package shim

import (
	"fmt"
	"strings"
)

// Host is everything the rewriting reads from the machine it runs on. The
// shim's main fills it from the real host; tests fill it with a platform and
// fakes, so the rewriting for every platform runs anywhere with no zig.
type Host struct {
	Platform Platform
	Getenv   func(string) string
	// ReadFile reads the host files the Linux glibc floor check looks at.
	ReadFile func(string) ([]byte, error)
	// SDK resolves the darwin SDK. Its Getenv is the same as Host's.
	SDK SDKProbe
	// TempOutput returns a fresh path under $TMPDIR with the given suffix,
	// for a compile to /dev/null whose output would otherwise land in the
	// working directory.
	TempOutput func(suffix string) (string, error)
}

// Plan is what the shim replaces itself with: zig's arguments after the zig
// executable itself.
type Plan struct {
	Args []string
	// Target is the zig -target, empty for ar and ranlib.
	Target string
	// SDK is the darwin SDK passed as --sysroot, empty for other targets.
	SDK string
}

// Rewrite turns one invocation of a role into zig's command line.
//
// For a compile role it supplies -target from the environment, checks the
// glibc floor on a Linux host building for Linux, and for a darwin target
// adds the SDK flags and de-duplicates -framework X and -lX. For an x86_64
// target it respells gcc's -march=x86-64[-vN] as zig's. On every platform, a
// compile to /dev/null is given an output under $TMPDIR. Nothing else is
// added: no host search path, and none of the rewrites of Uber's wrapper
// (-u SYMBOL, -l:file, C++'s default UBSan), which are adopted only where a
// measurement hits them.
func Rewrite(role Role, args []string, h Host) (Plan, error) {
	if !role.Compiles() {
		return Plan{Args: append([]string{string(role.Driver)}, args...)}, nil
	}
	target, triple, err := Target(role, h.Platform, h.Getenv)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Target: triple, Args: []string{string(role.Driver), "-target", triple}}
	if h.Platform.OS == "linux" && target.OS == "linux" {
		v, src, looked := HostGlibc(h.Platform.Arch, h.ReadFile)
		if err := CheckGlibcFloor(role.Name, h.Getenv(EnvGlibc), v, src, looked); err != nil {
			return Plan{}, err
		}
	}
	if target.OS == "darwin" {
		sdk, err := ResolveSDK(role.Name, h.SDK)
		if err != nil {
			return Plan{}, err
		}
		plan.SDK = sdk
		plan.Args = append(plan.Args, DarwinFlags(sdk)...)
		args = DedupDarwin(args)
	}
	if target.Arch == "amd64" {
		args = MarchX86(args)
	}
	args, err = DevNullOutput(args, h.TempOutput)
	if err != nil {
		return Plan{}, err
	}
	plan.Args = append(plan.Args, args...)
	return plan, nil
}

// takesValue is the driver flags whose value is the next argument. The
// rewriting skips those values, so a value is never mistaken for an input or
// a library.
var takesValue = map[string]bool{
	"-o": true, "-x": true, "-I": true, "-L": true, "-F": true, "-D": true, "-U": true,
	"-include": true, "-imacros": true, "-isystem": true, "-idirafter": true,
	"-iquote": true, "-isysroot": true, "-iprefix": true, "-iwithprefix": true,
	"--sysroot": true, "-MF": true, "-MT": true, "-MQ": true, "-MJ": true,
	"-target": true, "-arch": true, "-mllvm": true,
	"-Xlinker": true, "-Xclang": true, "-Xassembler": true, "-Xpreprocessor": true,
	"-weak_framework": true, "-install_name": true, "-rpath": true,
	"-z": true, "-T": true, "-e": true, "-u": true,
}

// DedupDarwin drops every repeat of -framework X and of -lX (or -l X),
// keeping the first. zig's Mach-O linker links a repeated dylib twice, and
// dyld then aborts with "duplicate linked dylib" (kottera ADR-155, Where zig
// cc broke, item 1). It is for darwin targets only: on ELF a repeated static
// library can be load-bearing.
func DedupDarwin(args []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		var key string
		width := 1
		switch {
		case a == "-framework" && i+1 < len(args):
			key, width = "framework "+args[i+1], 2
		case a == "-l" && i+1 < len(args):
			key, width = "l "+args[i+1], 2
		case strings.HasPrefix(a, "-l") && len(a) > 2:
			key = "l " + a[2:]
		case takesValue[a] && i+1 < len(args):
			width = 2
		}
		if key != "" && seen[key] {
			i += width - 1
			continue
		}
		if key != "" {
			seen[key] = true
		}
		out = append(out, args[i:i+width]...)
		i += width - 1
	}
	return out
}

// marchX86 is gcc's and clang's x86-64 micro-architecture levels, spelled as
// zig spells them.
var marchX86 = map[string]string{
	"x86-64":    "x86_64",
	"x86-64-v2": "x86_64_v2",
	"x86-64-v3": "x86_64_v3",
	"x86-64-v4": "x86_64_v4",
}

// MarchX86 respells -march=x86-64, -march=x86-64-v2, -march=x86-64-v3 and
// -march=x86-64-v4 as zig's x86_64, x86_64_v2, x86_64_v3 and x86_64_v4. zig 0.17.0 rejects the gcc spelling with
// "unknown CPU: 'x86'" (kottera W1-A2, divergence row 21), and the Rust cc
// crate passes -march=<target-cpu> in it. Every other -march value is
// returned unchanged. It is for x86_64 targets only.
func MarchX86(args []string) []string {
	out := append([]string{}, args...)
	for i := 0; i < len(out); i++ {
		a := out[i]
		if takesValue[a] {
			i++
			continue
		}
		if level, ok := strings.CutPrefix(a, "-march="); ok && marchX86[level] != "" {
			out[i] = "-march=" + marchX86[level]
		}
	}
	return out
}

// DevNullOutput gives a compile that writes to /dev/null an explicit output
// from tempOutput instead. `zig cc` does not honour -o /dev/null: it writes
// <basename>.o (or .s) into the working directory, and ./-.o for stdin input.
// Go's compiler probes run exactly `cc ... -c - -o /dev/null` (kottera
// ADR-155, Where zig cc broke, item 5), and kottera W1-A2 measured the
// general case with zig 0.17.0 on x86_64 Linux: `-o /dev/null` leaks for any
// input, one source file or several.
//
// It also gives one to a stdin compile under -### that names no output at
// all. Go identifies its C compiler with `cc -### -x c -c -`, run in the
// package's directory. clang only prints the commands it would run; `zig cc`
// runs them and writes ./-.o, which a cgo build left in a kottera checkout
// (measured with zig 0.17.0 on darwin/arm64 by this repository's integration
// test). Go reads only the version line of that output, so an added -o does
// not change the compiler's identity.
//
// Any other invocation is returned unchanged.
func DevNullOutput(args []string, tempOutput func(string) (string, error)) ([]string, error) {
	stdin, dryRun, named, out := false, false, false, -1
	suffix := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-":
			stdin = true
		case a == "-###":
			dryRun = true
		case a == "-o" && i+1 < len(args):
			named = true
			if args[i+1] == "/dev/null" {
				out = i
			}
			i++
		case a == "-o/dev/null":
			named = true
			out = i
		case strings.HasPrefix(a, "-o"):
			named = true
		case a == "-c":
			suffix = ".o"
		case a == "-S" && suffix == "":
			suffix = ".s"
		case a == "-E" && suffix == "":
			suffix = ".i"
		case takesValue[a]:
			i++
		}
	}
	unnamedDryRun := stdin && dryRun && !named
	if out < 0 && !unnamedDryRun {
		return args, nil
	}
	path, err := tempOutput(suffix)
	if err != nil {
		return nil, fmt.Errorf("zig-cc-shim: creating an output under $TMPDIR, so that zig writes nothing into the working directory: %w", err)
	}
	if unnamedDryRun {
		return append(append([]string{}, args...), "-o", path), nil
	}
	rewritten := append([]string{}, args[:out]...)
	rewritten = append(rewritten, "-o", path)
	if args[out] == "-o" {
		return append(rewritten, args[out+2:]...), nil
	}
	return append(rewritten, args[out+1:]...), nil
}
