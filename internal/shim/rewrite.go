package shim

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
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
	// working directory. On the real host it is TempOutputs.Create, which
	// also removes the outputs of compiles that are over.
	TempOutput func(suffix string) (string, error)
	// Getwd returns the working directory zig will run in, with every
	// symlink resolved.
	Getwd func() (string, error)
	// EvalSymlinks resolves every symlink in a path, failing if it does not
	// exist.
	EvalSymlinks func(string) (string, error)
	// IsDir reports whether a path is a directory.
	IsDir func(string) bool
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
// For a compile role it supplies -target from the environment, drops a
// caller's --target that restates it (and refuses one that does not), checks
// the glibc floor on a Linux host building for Linux, and for a darwin target
// adds the SDK flags, keeps a caller's absolute -L and -F that name a
// directory outside the SDK (given directly or in an @file),
// de-duplicates -framework X and -lX, and links -bundle as -shared (refusing
// a -bundle_loader, which only a bundle honours). For an x86_64
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
	args, err = CallerTarget(role, target, triple, args)
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
		args, err = CallerSearchDirs(args, sdk, h)
		if err != nil {
			return Plan{}, err
		}
		args = DedupDarwin(args)
		args, err = BundleAsDylib(role.Name, args, h.ReadFile)
		if err != nil {
			return Plan{}, err
		}
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
	"-weak_framework": true, "-bundle_loader": true, "-install_name": true, "-rpath": true,
	"-z": true, "-T": true, "-e": true, "-u": true,
}

// CallerTarget drops every --target=T, --target T and -target T from args
// when T names the same architecture and OS as target, the role's own. The
// Rust cc crate always passes one, in LLVM's spelling, which zig 0.17.0
// rejects: kottera W1-A3 measured --target=x86_64-unknown-linux-gnu
// ("UnknownOperatingSystem") and W1-A4 --target=arm64-apple-macosx
// ("unknown architecture: 'arm64'"). The shim's own -target already says the
// same thing, so the caller's only restates it. A version in T (macosx13.0,
// linux-gnu.2.17) is dropped with it: the glibc floor and the macOS minimum
// come only from CC_TOOLCHAIN_TARGET_*.
//
// A T that names another architecture, OS or ABI, or that is not a triple
// the shim can read, is refused rather than dropped, naming both targets
// and the role that builds T if there is one. Building for the role's target
// when the caller asked for another would be silent, and so would passing T
// on to override the role's.
func CallerTarget(role Role, target Platform, triple string, args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		var t string
		switch {
		case strings.HasPrefix(a, "--target="):
			t = strings.TrimPrefix(a, "--target=")
		case (a == "--target" || a == "-target") && i+1 < len(args):
			i++
			t = args[i]
		case takesValue[a] && i+1 < len(args):
			out = append(out, a, args[i+1])
			i++
			continue
		default:
			out = append(out, a)
			continue
		}
		if err := checkCallerTarget(role, target, triple, t); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// tripleArch is the architecture spellings of LLVM, Rust, Apple and zig.
var tripleArch = map[string]string{
	"x86_64": "amd64", "amd64": "amd64",
	"aarch64": "arm64", "arm64": "arm64",
}

// tripleVendor is the vendors a triple may name between its architecture
// and its OS.
var tripleVendor = map[string]bool{"unknown": true, "pc": true, "apple": true}

// tripleOS is the OS spellings of LLVM, Rust, Apple and zig, with any
// version stripped.
var tripleOS = map[string]string{
	"linux": "linux", "darwin": "darwin", "macos": "darwin", "macosx": "darwin",
}

// parseTriple reads arch-[vendor-]os[-abi], where os and abi may carry a
// version (macosx13.0, darwin23.1.0, macos.13.0, gnu.2.31). It returns ""
// when t names want, and otherwise the first component that differs from
// want's or that it cannot read: "architecture", "OS" or "ABI".
func parseTriple(t string, want Platform) (differs string) {
	parts := strings.Split(t, "-")
	arch, ok := tripleArch[parts[0]]
	if !ok || arch != want.Arch {
		return "architecture"
	}
	rest := parts[1:]
	if len(rest) > 1 && tripleVendor[rest[0]] {
		rest = rest[1:]
	}
	if len(rest) == 0 || len(rest) > 2 {
		return "OS"
	}
	goos, ok := tripleOS[strings.TrimRight(rest[0], "0123456789.")]
	if !ok || goos != want.OS {
		return "OS"
	}
	abi := ""
	if len(rest) == 2 {
		abi = strings.TrimRight(rest[1], "0123456789.")
	}
	if abi != "" && !(goos == "linux" && abi == "gnu") {
		return "ABI"
	}
	return ""
}

func checkCallerTarget(role Role, target Platform, triple, t string) error {
	differs := parseTriple(t, target)
	if differs == "" {
		return nil
	}
	differs = "it names a different " + differs
	if !strings.Contains(t, "-") {
		differs = "it is not a triple the shim can read"
	}
	repair := "no role of the shim builds " + quote(t) + ": its roles target " + strings.Join(tripleNames(), ", ") + " only; build it with another toolchain, or drop the --target to build for " + triple
	for name, p := range Triples {
		if parseTriple(t, p) == "" {
			repair = "invoke " + name + "-" + string(role.Driver) + " for " + quote(t) + " (for Cargo, name it in that target's CARGO_TARGET_<TRIPLE>_LINKER and CC_<triple>), or drop the --target to build for " + triple
		}
	}
	return &Refusal{
		Role:    role.Name,
		Missing: fmt.Sprintf("a --target this role builds: the caller asked for %s, but %s targets %s, and %s", quote(t), role.Name, triple, differs),
		Why:     "the shim takes its target only from the role and CC_TOOLCHAIN_TARGET_* (kottera ADR-155 D2, D3), so a caller's --target may restate that target but not change it; building for the role's target or for the caller's would each be silently building something the other did not ask for",
		Repair:  repair,
	}
}

func tripleNames() []string {
	var names []string
	for t := range Triples {
		names = append(names, t)
	}
	sort.Strings(names)
	return names
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

// BundleAsDylib replaces every -bundle with -shared on a darwin target.
// zig 0.17.0 does not link a Mach-O bundle: it links an executable, so a
// plain -bundle fails with "undefined symbol: _main", and node-gyp's macOS
// module link, `-bundle -undefined dynamic_lookup`, crashes zig's Mach-O
// linker with a segmentation fault (kottera W3-A3, reproduced with zig
// invoked directly). zig links -shared as an MH_DYLIB, which dlopen loads as
// it loads a bundle, so node require()s it and its undefined symbols resolve
// against node under -undefined dynamic_lookup.
//
// What the rewrite changes is the file type: the output is MH_DYLIB, not
// MH_BUNDLE, and carries an install name (its -o path). A loader that
// accepts only a bundle (the long-deprecated NSCreateObjectFileImageFromFile)
// would not load it. And a bundle alone may link against an executable's
// symbols with -bundle_loader, which a dylib cannot honour, so -bundle with
// -bundle_loader is refused rather than linked as something the caller did
// not ask for. So is a -bundle inside an @file, which the shim cannot
// respell without rewriting the caller's file. -Wl,-bundle and -Xlinker
// -bundle are left alone: zig 0.17.0 itself refuses them ("unsupported
// linker arg"). Every other argument is returned unchanged.
func BundleAsDylib(roleName string, args []string, readFile func(string) ([]byte, error)) ([]string, error) {
	bundle, loader, file := false, "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-bundle":
			bundle = true
		case a == "-bundle_loader" && i+1 < len(args):
			loader = args[i+1]
			i++
		case strings.HasPrefix(a, "@") && len(a) > 1 && file == "":
			if content, err := readFile(a[1:]); err == nil && slices.Contains(ResponseFileArgs(string(content)), "-bundle") {
				file = a
			}
		case takesValue[a]:
			i++
		}
	}
	why := "zig 0.17.0 does not link a Mach-O bundle (it links -bundle as an executable, and crashes with -undefined dynamic_lookup), so the shim links -bundle as -shared, an MH_DYLIB, which dlopen loads as it loads a bundle"
	switch {
	case file != "":
		return nil, &Refusal{
			Role:    roleName,
			Missing: "a -bundle the shim can respell: " + quote(file) + " passes -bundle in a response file",
			Why:     why + "; the shim respells only its own command line and never rewrites the caller's file",
			Repair:  "pass -bundle on the command line rather than in " + quote(file) + ", or link with -shared",
		}
	case bundle && loader != "":
		return nil, &Refusal{
			Role:    roleName,
			Missing: "an MH_BUNDLE for -bundle_loader " + quote(loader) + ": the caller asked for a bundle that links against that executable's symbols",
			Why:     why + "; only a bundle can name a -bundle_loader, so linking a dylib instead would silently drop it",
			Repair:  "drop -bundle_loader " + quote(loader) + " and link with -undefined dynamic_lookup, so the symbols resolve against the loading executable at load time, or build the bundle with Apple's toolchain",
		}
	case !bundle:
		return args, nil
	}
	out := make([]string, len(args))
	for i := 0; i < len(args); i++ {
		out[i] = args[i]
		if args[i] == "-bundle" {
			out[i] = "-shared"
		} else if takesValue[args[i]] && i+1 < len(args) {
			i++
			out[i] = args[i]
		}
	}
	return out, nil
}

// CallerSearchDirs keeps a caller's absolute -L and -F on a darwin target.
// Under --sysroot, zig joins every absolute -L and -F onto the SDK and drops
// the result when it does not exist (kottera W1-A4, with zig 0.17.0), so
// `#cgo LDFLAGS: -L${SRCDIR}/lib`, a Homebrew prefix or a Rust build script's
// OUT_DIR is never searched. zig leaves a relative directory alone and opens
// it from the working directory. So a -L<dir>, -L <dir>, -F<dir> or -F <dir>
// whose dir is absolute, exists, lies outside the SDK, and has no
// counterpart <sdk><dir> is respelled as the same directory relative to the
// working directory, keeping the caller's form (kottera PLAN-2026-10-hermetic-
// c-toolchain, ruling R2).
//
// A build system that passes its arguments in an @file response file passes
// its -L and -F there too, and zig re-roots those the same way. The shim
// leaves the @file argument and the file itself as the caller wrote them, and
// adds the respelled form of each such -L and -F in the file right after the
// @file argument, reading the file as zig does (ResponseFileArgs). That only
// adds: zig still reads the file and still drops the re-rooted original, so
// the kept directory is searched after the file's other directories rather
// than among them.
//
// The new spelling is derived only from the caller's argument and the
// working directory: the shim names no host path of its own (ADR-155 D4).
// Everything else is returned unchanged and left to zig: a relative
// directory, one inside the SDK, one the SDK also has (so -L/usr/lib still
// means <sdk>/usr/lib), and one that does not exist, which zig re-roots and
// drops as before.
func CallerSearchDirs(args []string, sdk string, h Host) ([]string, error) {
	k := searchDirKeeper{sdk: sdk, h: h}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-L" || a == "-F") && i+1 < len(args):
			i++
			kept, err := k.respell(a, args[i], false)
			if err != nil {
				return nil, err
			}
			if kept == nil {
				kept = []string{a, args[i]}
			}
			out = append(out, kept...)
		case strings.HasPrefix(a, "-L") || strings.HasPrefix(a, "-F"):
			kept, err := k.respell(a[:2], a[2:], true)
			if err != nil {
				return nil, err
			}
			if kept == nil {
				kept = []string{a}
			}
			out = append(out, kept...)
		case strings.HasPrefix(a, "@") && len(a) > 1:
			kept, err := k.responseFile(a[1:])
			if err != nil {
				return nil, err
			}
			out = append(append(out, a), kept...)
		case takesValue[a] && i+1 < len(args):
			out = append(out, a, args[i+1])
			i++
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

// searchDirKeeper respells the directories CallerSearchDirs keeps. It asks
// for the working directory only once there is one to keep.
type searchDirKeeper struct {
	sdk string
	h   Host
	cwd string
}

// respell returns flag and dir, joined or separate as the caller wrote them,
// with dir relative to the working directory, or nil when zig is left to
// handle dir.
func (k *searchDirKeeper) respell(flag, dir string, joined bool) ([]string, error) {
	if !keepCallerDir(dir, k.sdk, k.h) {
		return nil, nil
	}
	if k.cwd == "" {
		wd, err := k.h.Getwd()
		if err != nil {
			return nil, fmt.Errorf("zig-cc-shim: finding the working directory, to keep %s%s from being re-rooted under the SDK: %w", flag, dir, err)
		}
		k.cwd = wd
	}
	real, _ := k.h.EvalSymlinks(dir)
	rel, err := filepath.Rel(k.cwd, real)
	if err != nil {
		return nil, fmt.Errorf("zig-cc-shim: spelling %s%s relative to %s: %w", flag, dir, k.cwd, err)
	}
	if strings.HasPrefix(rel, "-") {
		rel = "./" + rel
	}
	if joined {
		return []string{flag + rel}, nil
	}
	return []string{flag, rel}, nil
}

// responseFile returns the respelled form of every -L and -F in the response
// file at path that respell keeps, in the file's order. A file the shim
// cannot read adds nothing: zig reads it too, and reports it. An @file named
// inside the file is not followed.
func (k *searchDirKeeper) responseFile(path string) ([]string, error) {
	content, err := k.h.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	args := ResponseFileArgs(string(content))
	var kept []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		var r []string
		switch {
		case (a == "-L" || a == "-F") && i+1 < len(args):
			i++
			r, err = k.respell(a, args[i], false)
		case strings.HasPrefix(a, "-L") || strings.HasPrefix(a, "-F"):
			r, err = k.respell(a[:2], a[2:], true)
		case takesValue[a]:
			i++
		}
		if err != nil {
			return nil, err
		}
		kept = append(kept, r...)
	}
	return kept, nil
}

// ResponseFileArgs splits a response file into arguments the way zig 0.17.0
// reads an @file on a `zig cc` command line: std.process.Args.IteratorGeneral
// with comments and single quotes (lib/std/process/Args.zig). Arguments are
// separated by spaces, tabs, carriage returns and newlines outside quotes. A
// # where an argument would begin starts a comment that runs to the end of
// the line. ' and " each open and close a quote, the one closing the other.
// Backslashes are literal except before a quote: 2n backslashes and a quote
// are n backslashes and the quote's effect, and 2n+1 backslashes and a quote
// are n backslashes and a literal ".
func ResponseFileArgs(s string) []string {
	var args []string
	i := 0
	for {
		// Skip whitespace and comments.
		for i < len(s) {
			switch s[i] {
			case ' ', '\t', '\r', '\n':
				i++
				continue
			case '#':
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			break
		}
		if i == len(s) {
			return args
		}
		var arg strings.Builder
		backslashes, inQuote := 0, false
	token:
		for ; i < len(s); i++ {
			c := s[i]
			switch c {
			case '"', '\'':
				arg.WriteString(strings.Repeat("\\", backslashes/2))
				if backslashes%2 == 0 {
					inQuote = !inQuote
				} else {
					arg.WriteByte('"')
				}
				backslashes = 0
			case '\\':
				backslashes++
			case ' ', '\t', '\r', '\n':
				arg.WriteString(strings.Repeat("\\", backslashes))
				backslashes = 0
				if !inQuote {
					break token
				}
				arg.WriteByte(c)
			default:
				arg.WriteString(strings.Repeat("\\", backslashes))
				backslashes = 0
				arg.WriteByte(c)
			}
		}
		arg.WriteString(strings.Repeat("\\", backslashes))
		args = append(args, arg.String())
	}
}

// keepCallerDir reports whether zig would lose dir under --sysroot sdk while
// the host has it: dir is absolute, is a directory outside the SDK, and
// <sdk><dir> is not a directory.
func keepCallerDir(dir, sdk string, h Host) bool {
	if !filepath.IsAbs(dir) || h.IsDir(filepath.Join(sdk, dir)) {
		return false
	}
	real, err := h.EvalSymlinks(dir)
	if err != nil || !h.IsDir(real) {
		return false
	}
	for _, root := range []string{sdk, resolvedOr(sdk, h)} {
		for _, p := range []string{filepath.Clean(dir), real} {
			if within(p, root) {
				return false
			}
		}
	}
	return true
}

// within reports whether p is root or lies beneath it.
func within(p, root string) bool {
	root = filepath.Clean(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func resolvedOr(p string, h Host) string {
	if r, err := h.EvalSymlinks(p); err == nil {
		return r
	}
	return p
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
