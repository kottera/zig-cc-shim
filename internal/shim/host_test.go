package shim

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeFS is an SDKProbe.Dir over a fake filesystem: dirs are directories,
// denied are present but unreadable (a sandbox's EPERM), and anything else is
// absent.
func fakeFS(dirs, denied []string) func(string) error {
	return func(p string) error {
		for _, d := range denied {
			if d == p {
				return &fs.PathError{Op: "stat", Path: p, Err: syscall.EPERM}
			}
		}
		for _, d := range dirs {
			if d == p {
				return nil
			}
		}
		return &fs.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
	}
}

func TestResolveSDK(t *testing.T) {
	const (
		fromEnv   = "/env/SDK"
		otherApp  = "/Volumes/Tools/Xcode-26.6.app"
		otherXc   = otherApp + "/Contents/Developer"
		otherSDK  = otherXc + "/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk"
		emptyDev  = "/opt/empty-developer-dir"
		cltViaDev = CommandLineToolsDeveloperDir
	)
	both := []string{CommandLineToolsSDK, XcodeSDK}
	cases := []struct {
		name   string
		env    map[string]string
		dirs   []string
		denied []string
		want   string
		// For a refusal: the text it must carry, and what it must not.
		refusal []string
		not     []string
	}{
		{name: "SDKROOT set and valid wins over everything",
			env: map[string]string{"SDKROOT": fromEnv, "DEVELOPER_DIR": XcodeDeveloperDir}, dirs: append([]string{fromEnv}, both...), want: fromEnv},
		{name: "SDKROOT set and missing is refused, naming the SDK it would otherwise take",
			env: map[string]string{"SDKROOT": "/gone/SDK"}, dirs: both,
			refusal: []string{"/gone/SDK", "is not a directory", "unset SDKROOT, and the shim takes " + CommandLineToolsSDK},
			not:     []string{"xcode-select --install"}},
		{name: "SDKROOT set and missing on a host with no SDK",
			env:     map[string]string{"SDKROOT": "/gone/SDK"},
			refusal: []string{"/gone/SDK", "point SDKROOT at an installed SDK", "xcode-select --install"}},
		{name: "SDKROOT set and missing, DEVELOPER_DIR naming no SDK",
			env: map[string]string{"SDKROOT": "/gone/SDK", "DEVELOPER_DIR": emptyDev}, dirs: both,
			refusal: []string{"/gone/SDK", "point SDKROOT at an installed SDK, or unset it"},
			not:     []string{"xcode-select --install", "the shim takes"}},
		{name: "SDKROOT set and unreadable is refused, naming the grant",
			env: map[string]string{"SDKROOT": fromEnv}, dirs: both, denied: []string{fromEnv},
			refusal: []string{fromEnv, "cannot be read", "grant this build read access to " + fromEnv},
			not:     []string{"xcode-select --install"}},
		{name: "DEVELOPER_DIR selecting Xcode, CLT present too",
			env: map[string]string{"DEVELOPER_DIR": XcodeDeveloperDir}, dirs: both, want: XcodeSDK},
		{name: "DEVELOPER_DIR selecting an Xcode.app bundle elsewhere",
			env: map[string]string{"DEVELOPER_DIR": otherApp + "/"}, dirs: append([]string{otherSDK}, both...), want: otherSDK},
		{name: "DEVELOPER_DIR selecting the Command Line Tools, Xcode present too",
			env: map[string]string{"DEVELOPER_DIR": cltViaDev}, dirs: both, want: CommandLineToolsSDK},
		{name: "DEVELOPER_DIR selecting a directory with no SDK never falls back",
			env: map[string]string{"DEVELOPER_DIR": emptyDev}, dirs: both,
			refusal: []string{"DEVELOPER_DIR", emptyDev, "set DEVELOPER_DIR to an Xcode", "or unset it"},
			not:     []string{"xcode-select --install", CommandLineToolsSDK}},
		{name: "DEVELOPER_DIR selecting an SDK it cannot read never falls back",
			env: map[string]string{"DEVELOPER_DIR": XcodeDeveloperDir}, dirs: []string{CommandLineToolsSDK}, denied: []string{XcodeSDK},
			refusal: []string{"grant this build read access to " + XcodeSDK},
			not:     []string{"xcode-select --install"}},
		{name: "Xcode only", dirs: []string{XcodeSDK}, want: XcodeSDK},
		{name: "Xcode only, unreadable",
			denied:  []string{XcodeSDK},
			refusal: []string{XcodeSDK, "present but cannot be read", "grant this build read access to " + XcodeSDK},
			not:     []string{"xcode-select --install"}},
		{name: "CLT only", dirs: []string{CommandLineToolsSDK}, want: CommandLineToolsSDK},
		{name: "both present, neither variable set: the Command Line Tools win", dirs: both, want: CommandLineToolsSDK},
		{name: "both present, CLT unreadable: refused, not skipped to Xcode",
			dirs: []string{XcodeSDK}, denied: []string{CommandLineToolsSDK},
			refusal: []string{"grant this build read access to " + CommandLineToolsSDK},
			not:     []string{"xcode-select --install"}},
		{name: "nothing",
			refusal: []string{"xcode-select --install", CommandLineToolsSDK, XcodeSDK, "SDKROOT and DEVELOPER_DIR are unset", "set DEVELOPER_DIR"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveSDK("cc", SDKProbe{
				Getenv: func(k string) string { return c.env[k] },
				Dir:    fakeFS(c.dirs, c.denied),
			})
			if c.refusal == nil {
				if err != nil || got != c.want {
					t.Fatalf("got %q, %v; want %q", got, err, c.want)
				}
				return
			}
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("got %q, %v; want a refusal", got, err)
			}
			for _, want := range c.refusal {
				if !strings.Contains(r.Error(), want) {
					t.Errorf("refusal lacks %q:\n%s", want, r)
				}
			}
			for _, not := range c.not {
				if strings.Contains(r.Repair, not) {
					t.Errorf("repair names %q:\n%s", not, r)
				}
			}
		})
	}
}

// Resolution reads the filesystem only (kottera PLAN-2026-10 R1): xcrun
// cannot run under a sandbox that grants just the SDKs directory.
func TestResolveSDKRunsNoSubprocess(t *testing.T) {
	src, err := os.ReadFile("sdk.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"os/exec"`, "StartProcess", "ForkExec", "syscall.Exec"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("sdk.go mentions %s", banned)
		}
	}
}

func TestStatDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := statDir(dir); err != nil {
		t.Errorf("a directory: %v", err)
	}
	for _, p := range []string{filepath.Join(dir, "missing"), file, filepath.Join(file, "below")} {
		if err := statDir(p); err == nil || !absent(err) {
			t.Errorf("%s: got %v, want absent", p, err)
		}
	}
}

func TestRewriteDarwinWithoutSDK(t *testing.T) {
	h := fakeHost(darwinArm, nil)
	h.SDK.Getenv = func(string) string { return "" }
	h.SDK.Dir = fakeFS(nil, nil)
	role, _ := ParseRole("cc")
	if _, err := Rewrite(role, []string{"-c", "a.c"}, h); err == nil || !strings.Contains(err.Error(), "xcode-select --install") {
		t.Errorf("got %v", err)
	}
	// A Linux target needs no SDK, even on darwin.
	linux, _ := ParseRole("x86_64-unknown-linux-gnu-cc")
	if _, err := Rewrite(linux, []string{"-c", "a.c"}, h); err != nil {
		t.Errorf("linux target on darwin without an SDK: %v", err)
	}
}

func TestSDKVersion(t *testing.T) {
	read := func(b string, err error) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p != fakeSDK+"/SDKSettings.json" {
				t.Errorf("read %s", p)
			}
			return []byte(b), err
		}
	}
	if v := SDKVersion(fakeSDK, read(`{"Version":"26.4","DisplayName":"macOS 26.4"}`, nil)); v != "26.4" {
		t.Errorf("got %q", v)
	}
	if v := SDKVersion(fakeSDK, read("", os.ErrNotExist)); v != "unknown" {
		t.Errorf("got %q", v)
	}
	if v := SDKVersion(fakeSDK, read("{", nil)); v != "unknown" {
		t.Errorf("got %q", v)
	}
}

func TestHostGlibc(t *testing.T) {
	files := map[string]string{
		"/lib/x86_64-linux-gnu/libc.so.6": "GNU C Library (Ubuntu GLIBC 2.43-0ubuntu1) stable release version 2.43.",
		"/lib/ld-linux-aarch64.so.1":      "ld.so (GNU libc) release version 2.28",
	}
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte("\x7fELF\x00" + s + "\x00"), nil
		}
		return nil, os.ErrNotExist
	}
	// amd64: the loader is absent, libc.so.6 answers.
	v, src, looked := HostGlibc("amd64", read)
	if v != "2.43" || src != "/lib/x86_64-linux-gnu/libc.so.6" || looked[0] != "/lib64/ld-linux-x86-64.so.2" {
		t.Errorf("amd64: %q %q %q", v, src, looked)
	}
	// arm64: the loader answers first.
	if v, src, _ := HostGlibc("arm64", read); v != "2.28" || src != "/lib/ld-linux-aarch64.so.1" {
		t.Errorf("arm64: %q %q", v, src)
	}
	// No glibc anywhere (a musl host).
	if v, _, looked := HostGlibc("amd64", func(string) ([]byte, error) { return []byte("musl"), nil }); v != "" || len(looked) != len(glibcFiles["amd64"]) {
		t.Errorf("musl: %q %q", v, looked)
	}
}

func TestCheckGlibcFloor(t *testing.T) {
	if err := CheckGlibcFloor("cc", "2.31", "2.43", "/lib64/ld-linux-x86-64.so.2", nil); err != nil {
		t.Errorf("above the floor: %v", err)
	}
	if err := CheckGlibcFloor("cc", "2.31", "2.31", "x", nil); err != nil {
		t.Errorf("at the floor: %v", err)
	}
	err := CheckGlibcFloor("cc", "2.31", "2.28", "/lib64/ld-linux-x86-64.so.2", nil)
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("below the floor not refused: %v", err)
	}
	for _, want := range []string{"2.28", "2.31", "/lib64/ld-linux-x86-64.so.2", "repair:", "operator"} {
		if !strings.Contains(r.Error(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, r)
		}
	}
	err = CheckGlibcFloor("cc", "2.31", "", "", []string{"/lib64/ld-linux-x86-64.so.2"})
	if err == nil || !strings.Contains(err.Error(), "/lib64/ld-linux-x86-64.so.2") {
		t.Errorf("unknown glibc: %v", err)
	}
}

func TestRewriteGlibcFloor(t *testing.T) {
	role, _ := ParseRole("cc")
	// The fake host's glibc is 2.39.
	if _, err := Rewrite(role, nil, fakeHost(linuxAmd, map[string]string{EnvGlibc: "2.40"})); err == nil || !strings.Contains(err.Error(), "2.39") {
		t.Errorf("below the floor on linux: %v", err)
	}
	// The check is a Linux host's: a darwin host building for Linux is not
	// checked, and neither is a Linux host building for darwin.
	cross, _ := ParseRole("x86_64-unknown-linux-gnu-cc")
	if _, err := Rewrite(cross, nil, fakeHost(darwinArm, map[string]string{EnvGlibc: "2.40"})); err != nil {
		t.Errorf("darwin host: %v", err)
	}
	mac, _ := ParseRole("aarch64-apple-darwin-cc")
	h := fakeHost(linuxAmd, nil)
	h.ReadFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	if _, err := Rewrite(mac, nil, h); err != nil {
		t.Errorf("linux host, darwin target: %v", err)
	}
	if _, err := Rewrite(role, nil, h); err == nil {
		t.Error("linux host with no readable glibc was not refused")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2.31", "2.31", 0}, {"2.9", "2.31", -1}, {"2.43", "2.31", 1},
		{"2.31.1", "2.31", 1}, {"2.31", "2.31.0", 0}, {"13", "13.0", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: %d", c.a, c.b, got)
		}
	}
}

func TestDescriptionFormat(t *testing.T) {
	darwin := Description{
		ShimVersion: "v0.1.0", ShimRevision: "abc", Role: "cc", Zig: "/s/cc/zig/zig",
		ZigVersion: "0.17.0", Target: "aarch64-macos.13.0", SDK: fakeSDK, SDKVersion: "26.4",
	}
	want := `zig-cc-shim-describe: 1
shim: v0.1.0
shim-revision: abc
role: cc
zig: /s/cc/zig/zig
zig-version: 0.17.0
target: aarch64-macos.13.0
sdk: /sdk/MacOSX.sdk
sdk-version: 26.4
`
	if got := darwin.String(); got != want {
		t.Errorf("darwin:\n%s\nwant:\n%s", got, want)
	}
	linux := Description{
		ShimVersion: "v0.1.0", ShimRevision: "abc", Role: "ar", Zig: "/s/cc/zig/zig",
		ZigVersion: "0.17.0", HostGlibc: "2.43",
	}
	want = `zig-cc-shim-describe: 1
shim: v0.1.0
shim-revision: abc
role: ar
zig: /s/cc/zig/zig
zig-version: 0.17.0
target: none
host-glibc: 2.43
`
	if got := linux.String(); got != want {
		t.Errorf("linux:\n%s\nwant:\n%s", got, want)
	}
}

func TestLocateZig(t *testing.T) {
	store := t.TempDir()
	mustWrite(t, filepath.Join(store, "cc", "zig", "zig"))
	mustWrite(t, filepath.Join(store, "cc", "bin", "cc"))
	// A role entry symlinked from elsewhere still finds the store's zig.
	elsewhere := t.TempDir()
	link := filepath.Join(elsewhere, "cc")
	if err := os.Symlink(filepath.Join(store, "cc", "bin", "cc"), link); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{filepath.Join(store, "cc", "bin", "cc"), link} {
		zig, err := LocateZig("cc", exe, filepath.EvalSymlinks, isFile)
		if err != nil {
			t.Fatalf("%s: %v", exe, err)
		}
		want, _ := filepath.EvalSymlinks(filepath.Join(store, "cc", "zig", "zig"))
		if zig != want {
			t.Errorf("%s: got %s want %s", exe, zig, want)
		}
	}
	// No zig: refused, naming the path looked at.
	bare := t.TempDir()
	mustWrite(t, filepath.Join(bare, "bin", "cc"))
	_, err := LocateZig("cc", filepath.Join(bare, "bin", "cc"), filepath.EvalSymlinks, isFile)
	resolved, _ := filepath.EvalSymlinks(bare)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(resolved, "zig", "zig")) {
		t.Errorf("missing zig: %v", err)
	}
	// A directory named zig is not zig.
	if err := os.MkdirAll(filepath.Join(bare, "zig", "zig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LocateZig("cc", filepath.Join(bare, "bin", "cc"), filepath.EvalSymlinks, isFile); err == nil {
		t.Error("a directory was taken for zig")
	}
}

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
}
