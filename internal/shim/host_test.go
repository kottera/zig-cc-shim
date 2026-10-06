package shim

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSDKOrder(t *testing.T) {
	const fromEnv, fromXcrun = "/env/SDK", "/xcrun/SDK"
	cases := []struct {
		name    string
		sdkroot string
		xcrun   string
		xErr    error
		dirs    []string
		want    string
	}{
		{"SDKROOT wins", fromEnv, fromXcrun, nil, []string{fromEnv, fromXcrun, CommandLineToolsSDK}, fromEnv},
		{"then xcrun", "", fromXcrun, nil, []string{fromXcrun, CommandLineToolsSDK}, fromXcrun},
		{"xcrun failing falls to CLT", "", "", errors.New("no developer dir"), []string{CommandLineToolsSDK}, CommandLineToolsSDK},
		{"xcrun naming a missing dir falls to CLT", "", fromXcrun, nil, []string{CommandLineToolsSDK}, CommandLineToolsSDK},
		{"xcrun printing nothing falls to CLT", "", "", nil, []string{CommandLineToolsSDK}, CommandLineToolsSDK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			xcrunCalled := false
			got, err := ResolveSDK("cc", SDKProbe{
				Getenv: func(k string) string {
					if k == "SDKROOT" {
						return c.sdkroot
					}
					return ""
				},
				IsDir: func(d string) bool {
					for _, x := range c.dirs {
						if x == d {
							return true
						}
					}
					return false
				},
				Xcrun: func() (string, error) { xcrunCalled = true; return c.xcrun, c.xErr },
			})
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
			if c.sdkroot != "" && xcrunCalled {
				t.Error("xcrun ran although SDKROOT was set")
			}
		})
	}
}

func TestResolveSDKRefusals(t *testing.T) {
	none := SDKProbe{
		Getenv: func(string) string { return "" },
		IsDir:  func(string) bool { return false },
		Xcrun:  func() (string, error) { return "", errors.New("xcrun: error: invalid active developer path") },
	}
	_, err := ResolveSDK("cc", none)
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("not refused: %v", err)
	}
	for _, want := range []string{"xcode-select --install", CommandLineToolsSDK, "SDKROOT is unset"} {
		if !strings.Contains(r.Error(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, r)
		}
	}
	// A set SDKROOT that is not a directory is refused, not skipped.
	badRoot := none
	badRoot.Getenv = func(k string) string {
		if k == "SDKROOT" {
			return "/gone/SDK"
		}
		return ""
	}
	badRoot.IsDir = func(d string) bool { return d == CommandLineToolsSDK }
	if _, err := ResolveSDK("cc", badRoot); err == nil || !strings.Contains(err.Error(), "/gone/SDK") {
		t.Errorf("a missing SDKROOT fell through: %v", err)
	}
}

func TestRewriteDarwinWithoutSDK(t *testing.T) {
	h := fakeHost(darwinArm, nil)
	h.SDK.IsDir = func(string) bool { return false }
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
