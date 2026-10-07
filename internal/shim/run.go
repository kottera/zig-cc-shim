package shim

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// LocateZig finds zig at <resolved self>/../zig/zig, the store layout of
// kottera ADR-155 D7 (cc/{zig/{zig,lib/},bin/}). exe is the shim's own path;
// symlinks in it are resolved first, so a symlinked role entry finds the zig
// beside the real binary. No absolute path is baked in or configured, so the
// store stays relocatable.
func LocateZig(role, exe string, evalSymlinks func(string) (string, error), isFile func(string) bool) (string, error) {
	resolved, err := evalSymlinks(exe)
	if err != nil {
		return "", &Refusal{
			Role:    role,
			Missing: "the shim's own location: resolving " + exe + ": " + err.Error(),
			Why:     "zig is found relative to the shim's resolved executable",
			Repair:  "invoke the shim through its entry in the store's bin/",
		}
	}
	zig := filepath.Join(filepath.Dir(resolved), "..", "zig", "zig")
	if !isFile(zig) {
		return "", &Refusal{
			Role:    role,
			Missing: "zig at " + zig + " (looked for ../zig/zig beside the shim at " + resolved + ")",
			Why:     "the shim fronts the zig in its own store, laid out as cc/{zig/zig, bin/}, and never looks for zig anywhere else",
			Repair:  "re-provision the `cc` toolchain with `tera toolchain sync` (an operator action); outside tera, unpack an official zig release so that " + zig + " exists",
		}
	}
	return zig, nil
}

// Main runs the shim as argv describes and returns an exit status if it does
// not replace itself with zig.
func Main(argv []string, stdout, stderr io.Writer) int {
	role, err := ParseRole(filepath.Base(argv[0]))
	if err != nil {
		return fail(stderr, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stderr, fmt.Errorf("zig-cc-shim (%s): finding its own executable: %w", role.Name, err))
	}
	zig, err := LocateZig(role.Name, exe, filepath.EvalSymlinks, isFile)
	if err != nil {
		return fail(stderr, err)
	}
	host := RealHost()
	if len(argv) == 2 && argv[1] == DescribeFlag {
		d, err := Describe(role, zig, host)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprint(stdout, d)
		return 0
	}
	plan, err := Rewrite(role, argv[1:], host)
	if err != nil {
		return fail(stderr, err)
	}
	// Only returns on failure: on success the process is zig, so signals and
	// the exit status are zig's.
	err = execve(zig, append([]string{zig}, plan.Args...), os.Environ())
	return fail(stderr, fmt.Errorf("zig-cc-shim (%s): replacing itself with %s: %w", role.Name, zig, err))
}

// Describe gathers what --tera-describe reports.
func Describe(role Role, zig string, h Host) (Description, error) {
	d := Description{Role: role.Name, Zig: zig}
	d.ShimVersion, d.ShimRevision = shimVersion()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, zig, "version").Output()
	if err != nil {
		return d, fmt.Errorf("zig-cc-shim (%s): running `%s version`: %w", role.Name, zig, err)
	}
	d.ZigVersion = strings.TrimSpace(string(out))
	if role.Compiles() {
		target, triple, err := Target(role, h.Platform, h.Getenv)
		if err != nil {
			return d, err
		}
		d.Target = triple
		if target.OS == "darwin" {
			if d.SDK, err = ResolveSDK(role.Name, h.SDK); err != nil {
				return d, err
			}
			d.SDKVersion = SDKVersion(d.SDK, os.ReadFile)
		}
	}
	if h.Platform.OS == "linux" {
		d.HostGlibc = "unknown"
		if v, _, _ := HostGlibc(h.Platform.Arch, h.ReadFile); v != "" {
			d.HostGlibc = v
		}
	}
	return d, nil
}

// RealHost is the Host the shim runs on.
func RealHost() Host {
	return Host{
		Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Getenv:   os.Getenv,
		ReadFile: os.ReadFile,
		SDK: SDKProbe{
			Getenv: os.Getenv,
			Dir:    statDir,
		},
		TempOutput: TempOutputs{
			Dir:   os.TempDir(),
			PID:   os.Getpid(),
			Now:   time.Now,
			Alive: pidAlive,
		}.Create,
		Getwd: func() (string, error) {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(wd)
		},
		EvalSymlinks: filepath.EvalSymlinks,
		IsDir:        func(p string) bool { return statDir(p) == nil },
	}
}

func shimVersion() (version, revision string) {
	version, revision = "unknown", "unknown"
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	version = bi.Main.Version
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			revision = s.Value
		}
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.modified" && s.Value == "true" && revision != "unknown" {
			revision += "+modified"
		}
	}
	return
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func fail(stderr io.Writer, err error) int {
	msg := err.Error()
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprint(stderr, msg)
	return 1
}
