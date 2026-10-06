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
	// for a stdin compile whose output would otherwise be ./-.o.
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
// adds the SDK flags and de-duplicates -framework X and -lX. On every
// platform, a stdin compile to /dev/null is given an output under $TMPDIR.
// Nothing else is added: no host search path, and none of the rewrites of
// Uber's wrapper (-u SYMBOL, -l:file, C++'s default UBSan), which are adopted
// only where a measurement hits them.
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
	args, err = StdinOutput(args, h.TempOutput)
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

// StdinOutput gives a compile that reads stdin and writes to /dev/null an
// explicit output from tempOutput instead. `zig cc` ignores -o for stdin input
// and writes ./-.o, and Go's compiler probes run exactly
// `cc ... -c - -o /dev/null` (kottera ADR-155, Where zig cc broke, item 5).
// Any other invocation is returned unchanged.
func StdinOutput(args []string, tempOutput func(string) (string, error)) ([]string, error) {
	stdin, out := false, -1
	suffix := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-":
			stdin = true
		case a == "-o" && i+1 < len(args):
			if args[i+1] == "/dev/null" {
				out = i
			}
			i++
		case a == "-o/dev/null":
			out = i
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
	if !stdin || out < 0 {
		return args, nil
	}
	path, err := tempOutput(suffix)
	if err != nil {
		return nil, fmt.Errorf("zig-cc-shim: creating an output under $TMPDIR for a stdin compile: %w", err)
	}
	rewritten := append([]string{}, args[:out]...)
	rewritten = append(rewritten, "-o", path)
	if args[out] == "-o" {
		return append(rewritten, args[out+2:]...), nil
	}
	return append(rewritten, args[out+1:]...), nil
}
