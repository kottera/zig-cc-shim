package shim

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fakeSDK = "/sdk/MacOSX.sdk"

var (
	darwinArm = Platform{OS: "darwin", Arch: "arm64"}
	darwinAmd = Platform{OS: "darwin", Arch: "amd64"}
	linuxAmd  = Platform{OS: "linux", Arch: "amd64"}
	linuxArm  = Platform{OS: "linux", Arch: "arm64"}
)

// fakeHost is a host on platform p with the seeded floor and minimum set, an
// SDK named by SDKROOT, and glibc 2.39 readable from the loader.
func fakeHost(p Platform, env map[string]string) Host {
	vars := map[string]string{EnvGlibc: "2.31", EnvMacOS: "13.0", "SDKROOT": fakeSDK}
	for k, v := range env {
		vars[k] = v
	}
	getenv := func(k string) string { return vars[k] }
	return Host{
		Platform: p,
		Getenv:   getenv,
		ReadFile: func(f string) ([]byte, error) {
			if f == glibcFiles[p.Arch][0] {
				return []byte("\x00ld.so (GNU libc) stable release version 2.39.\nCopyright"), nil
			}
			return nil, os.ErrNotExist
		},
		SDK: SDKProbe{
			Getenv: getenv,
			Dir: func(d string) error {
				if d == fakeSDK {
					return nil
				}
				return fs.ErrNotExist
			},
		},
		TempOutput:   func(suffix string) (string, error) { return "/tmpdir/zig-cc-shim-out-1" + suffix, nil },
		Getwd:        func() (string, error) { return fakeCwd, nil },
		EvalSymlinks: fakeEvalSymlinks,
		IsDir:        func(d string) bool { return fakeDirs[d] },
	}
}

// fakeCwd is the working directory of fakeHost, resolved.
const fakeCwd = "/private/var/work/pkg"

// fakeDirs is the directories of fakeHost, as resolved paths.
var fakeDirs = map[string]bool{
	fakeSDK: true, fakeSDK + "/usr/lib": true, fakeSDK + "/usr/include": true,
	fakeSDK + "/System/Library/Frameworks": true,
	"/usr/lib":                             true, "/System/Library/Frameworks": true,
	"/opt/homebrew/lib": true, "/opt/homebrew/Frameworks": true,
	"/private/var/work/pkg": true, "/private/var/work/pkg/lib": true, "/private/var/work/other/lib": true,
	"/-odd": true,
}

// fakeSymlinks is the symlinks of fakeHost: /var is /private/var, as on
// macOS.
var fakeSymlinks = map[string]string{"/var": "/private/var"}

func fakeEvalSymlinks(p string) (string, error) {
	p = filepath.Clean(p)
	for link, real := range fakeSymlinks {
		if rest, ok := strings.CutPrefix(p, link); ok && (rest == "" || rest[0] == '/') {
			p = real + rest
		}
	}
	if !fakeDirs[p] {
		return "", os.ErrNotExist
	}
	return p, nil
}

func darwinPrefix(driver, target string) []string {
	return []string{driver, "-target", target,
		"--sysroot", fakeSDK, "-idirafter", fakeSDK + "/usr/include",
		"-F/System/Library/Frameworks", "-L/usr/lib"}
}

type rewriteCase struct {
	host Platform
	role string
	want []string
}

func TestRewriteEveryRole(t *testing.T) {
	in := []string{"-O2", "-c", "a.c", "-o", "a.o"}
	cases := []rewriteCase{
		// Host roles on a Linux host: the target is the host's arch at the floor.
		{linuxAmd, "cc", []string{"cc", "-target", "x86_64-linux-gnu.2.31"}},
		{linuxAmd, "gcc", []string{"cc", "-target", "x86_64-linux-gnu.2.31"}},
		{linuxAmd, "clang", []string{"cc", "-target", "x86_64-linux-gnu.2.31"}},
		{linuxAmd, "c++", []string{"c++", "-target", "x86_64-linux-gnu.2.31"}},
		{linuxAmd, "g++", []string{"c++", "-target", "x86_64-linux-gnu.2.31"}},
		{linuxAmd, "clang++", []string{"c++", "-target", "x86_64-linux-gnu.2.31"}},
		{linuxArm, "cc", []string{"cc", "-target", "aarch64-linux-gnu.2.31"}},
		{linuxArm, "c++", []string{"c++", "-target", "aarch64-linux-gnu.2.31"}},
		// Host roles on a darwin host: the target is the host's arch at the
		// minimum, plus the SDK flags, verbatim.
		{darwinArm, "cc", darwinPrefix("cc", "aarch64-macos.13.0")},
		{darwinArm, "gcc", darwinPrefix("cc", "aarch64-macos.13.0")},
		{darwinArm, "clang", darwinPrefix("cc", "aarch64-macos.13.0")},
		{darwinArm, "c++", darwinPrefix("c++", "aarch64-macos.13.0")},
		{darwinArm, "g++", darwinPrefix("c++", "aarch64-macos.13.0")},
		{darwinArm, "clang++", darwinPrefix("c++", "aarch64-macos.13.0")},
		{darwinAmd, "cc", darwinPrefix("cc", "x86_64-macos.13.0")},
		// ar and ranlib take no target and add nothing, on either platform.
		{linuxAmd, "ar", []string{"ar"}},
		{linuxAmd, "ranlib", []string{"ranlib"}},
		{darwinArm, "ar", []string{"ar"}},
		{darwinArm, "ranlib", []string{"ranlib"}},
	}
	// Triple roles: the target is fixed by the triple, whatever the host.
	for _, host := range []Platform{linuxAmd, linuxArm, darwinArm, darwinAmd} {
		cases = append(cases,
			rewriteCase{host, "x86_64-unknown-linux-gnu-cc", []string{"cc", "-target", "x86_64-linux-gnu.2.31"}},
			rewriteCase{host, "x86_64-unknown-linux-gnu-c++", []string{"c++", "-target", "x86_64-linux-gnu.2.31"}},
			rewriteCase{host, "aarch64-unknown-linux-gnu-cc", []string{"cc", "-target", "aarch64-linux-gnu.2.31"}},
			rewriteCase{host, "aarch64-unknown-linux-gnu-c++", []string{"c++", "-target", "aarch64-linux-gnu.2.31"}},
			rewriteCase{host, "aarch64-apple-darwin-cc", darwinPrefix("cc", "aarch64-macos.13.0")},
			rewriteCase{host, "aarch64-apple-darwin-c++", darwinPrefix("c++", "aarch64-macos.13.0")},
			rewriteCase{host, "x86_64-apple-darwin-cc", darwinPrefix("cc", "x86_64-macos.13.0")},
			rewriteCase{host, "x86_64-apple-darwin-c++", darwinPrefix("c++", "x86_64-macos.13.0")},
		)
	}
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.role] = true
		t.Run(c.host.OS+"-"+c.host.Arch+"/"+c.role, func(t *testing.T) {
			role, err := ParseRole(c.role)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Rewrite(role, in, fakeHost(c.host, nil))
			if err != nil {
				t.Fatal(err)
			}
			want := append(append([]string{}, c.want...), in...)
			if !reflect.DeepEqual(plan.Args, want) {
				t.Errorf("args\n got %q\nwant %q", plan.Args, want)
			}
		})
	}
	for _, name := range RoleNames() {
		if !covered[name] {
			t.Errorf("role %s has no case", name)
		}
	}
}

func TestRewriteAddsNoHostSearchPath(t *testing.T) {
	// Kottera ADR-155 D4: the only host input is the SDK. Nothing names
	// /usr/include, /usr/local or Homebrew; on darwin the measured set is the
	// only addition.
	for _, host := range []Platform{linuxAmd, linuxArm, darwinArm, darwinAmd} {
		for _, name := range RoleNames() {
			role, _ := ParseRole(name)
			plan, err := Rewrite(role, nil, fakeHost(host, nil))
			if err != nil {
				t.Fatalf("%v %s: %v", host, name, err)
			}
			for _, a := range plan.Args {
				for _, bad := range []string{"/usr/include", "/usr/local", "/opt/homebrew", "/usr/lib/"} {
					if strings.HasPrefix(a, bad) {
						t.Errorf("%v %s: added host path %q", host, name, a)
					}
				}
			}
			if plan.SDK == "" && strings.Contains(strings.Join(plan.Args, " "), "/usr/lib") {
				t.Errorf("%v %s: -L/usr/lib without an SDK: %q", host, name, plan.Args)
			}
		}
	}
}

func TestDedupDarwin(t *testing.T) {
	cases := []struct {
		name     string
		in, want []string
	}{
		{"framework",
			[]string{"a.o", "-framework", "CoreFoundation", "-framework", "Security", "-framework", "CoreFoundation"},
			[]string{"a.o", "-framework", "CoreFoundation", "-framework", "Security"}},
		{"libraries",
			[]string{"-lresolv", "a.o", "-lresolv", "-lpthread", "-l", "resolv", "-l", "z", "-lz"},
			[]string{"-lresolv", "a.o", "-lpthread", "-l", "z"}},
		{"values are not libraries",
			[]string{"-Xlinker", "-lfoo", "-lfoo", "-o", "-lfoo"},
			[]string{"-Xlinker", "-lfoo", "-lfoo", "-o", "-lfoo"}},
		{"trailing -framework is kept",
			[]string{"-framework"},
			[]string{"-framework"}},
		{"nothing repeated",
			[]string{"-c", "a.c", "-o", "a.o"},
			[]string{"-c", "a.c", "-o", "a.o"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DedupDarwin(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestRewriteDedupIsDarwinOnly(t *testing.T) {
	in := []string{"a.o", "-lfoo", "-lbar", "-lfoo", "-framework", "X", "-framework", "X"}
	for _, c := range []rewriteCase{
		{darwinArm, "cc", []string{"a.o", "-lfoo", "-lbar", "-framework", "X"}},
		{linuxAmd, "aarch64-apple-darwin-cc", []string{"a.o", "-lfoo", "-lbar", "-framework", "X"}},
		{linuxAmd, "cc", in},
		{darwinArm, "x86_64-unknown-linux-gnu-cc", in},
		{darwinArm, "ar", in},
	} {
		role, _ := ParseRole(c.role)
		plan, err := Rewrite(role, in, fakeHost(c.host, nil))
		if err != nil {
			t.Fatal(err)
		}
		got := plan.Args[len(plan.Args)-len(c.want):]
		if !reflect.DeepEqual(got, c.want) || len(plan.Args) < len(c.want) {
			t.Errorf("%v %s: got %q want suffix %q", c.host, c.role, plan.Args, c.want)
		}
	}
}

func TestDevNullOutput(t *testing.T) {
	temp := func(suffix string) (string, error) { return "/tmpdir/out" + suffix, nil }
	cases := []struct {
		name     string
		in, want []string
	}{
		{"go's probe",
			[]string{"-Werror", "-x", "c", "-c", "-", "-o", "/dev/null"},
			[]string{"-Werror", "-x", "c", "-c", "-", "-o", "/tmpdir/out.o"}},
		{"output before input",
			[]string{"-c", "-o", "/dev/null", "-x", "c", "-"},
			[]string{"-c", "-o", "/tmpdir/out.o", "-x", "c", "-"}},
		{"joined -o",
			[]string{"-x", "c", "-c", "-", "-o/dev/null"},
			[]string{"-x", "c", "-c", "-", "-o", "/tmpdir/out.o"}},
		{"link from stdin",
			[]string{"-x", "c", "-", "-o", "/dev/null"},
			[]string{"-x", "c", "-", "-o", "/tmpdir/out"}},
		{"preprocess",
			[]string{"-E", "-x", "c", "-", "-o", "/dev/null"},
			[]string{"-E", "-x", "c", "-", "-o", "/tmpdir/out.i"}},
		{"go's compiler identification",
			[]string{"-###", "-x", "c", "-c", "-"},
			[]string{"-###", "-x", "c", "-c", "-", "-o", "/tmpdir/out.o"}},
		{"-### with a named output",
			[]string{"-###", "-x", "c", "-c", "-", "-o", "b.o"},
			[]string{"-###", "-x", "c", "-c", "-", "-o", "b.o"}},
		{"-### with a joined output",
			[]string{"-###", "-x", "c", "-c", "-", "-ob.o"},
			[]string{"-###", "-x", "c", "-c", "-", "-ob.o"}},
		{"-### without stdin",
			[]string{"-###", "-c", "a.c"},
			[]string{"-###", "-c", "a.c"}},
		{"stdin without -### or -o is left to zig",
			[]string{"-x", "c", "-c", "-"},
			[]string{"-x", "c", "-c", "-"}},
		{"not stdin",
			[]string{"-c", "a.c", "-o", "/dev/null"},
			[]string{"-c", "a.c", "-o", "/tmpdir/out.o"}},
		{"stdin to a real output",
			[]string{"-x", "c", "-c", "-", "-o", "b.o"},
			[]string{"-x", "c", "-c", "-", "-o", "b.o"}},
		{"- as -o's value is not stdin",
			[]string{"-E", "a.c", "-o", "-"},
			[]string{"-E", "a.c", "-o", "-"}},
		{"- as a flag's value is not stdin",
			[]string{"-###", "-c", "a.c", "-MF", "-"},
			[]string{"-###", "-c", "a.c", "-MF", "-"}},
		{"/dev/null as a flag's value is not the output",
			[]string{"-c", "a.c", "-MF", "/dev/null", "-o", "a.o"},
			[]string{"-c", "a.c", "-MF", "/dev/null", "-o", "a.o"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DevNullOutput(c.in, temp)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
	if _, err := DevNullOutput([]string{"-c", "-", "-o", "/dev/null"}, func(string) (string, error) {
		return "", errors.New("no space")
	}); err == nil || !strings.Contains(err.Error(), "$TMPDIR") {
		t.Errorf("a failed temp output is not reported: %v", err)
	}
}

func TestRewriteStdinOnBothPlatforms(t *testing.T) {
	probe := []string{"-x", "c", "-c", "-", "-o", "/dev/null"}
	for _, host := range []Platform{linuxAmd, darwinArm} {
		role, _ := ParseRole("cc")
		plan, err := Rewrite(role, probe, fakeHost(host, nil))
		if err != nil {
			t.Fatal(err)
		}
		tail := plan.Args[len(plan.Args)-2:]
		if !reflect.DeepEqual(tail, []string{"-o", "/tmpdir/zig-cc-shim-out-1.o"}) {
			t.Errorf("%v: %q", host, plan.Args)
		}
	}
}

// TestRewriteDevNullOutput is kottera W1-A2's finding (4): with zig 0.17.0,
// `-o /dev/null` leaks <basename>.o into the working directory whatever the
// input, not only for stdin.
func TestRewriteDevNullOutput(t *testing.T) {
	const tmp = "/tmpdir/zig-cc-shim-out-1"
	cases := []struct {
		name     string
		in, want []string
	}{
		{"one source file",
			[]string{"-c", "a.c", "-o", "/dev/null"},
			[]string{"-c", "a.c", "-o", tmp + ".o"}},
		{"several source files",
			[]string{"-c", "a.c", "b.c", "-o", "/dev/null"},
			[]string{"-c", "a.c", "b.c", "-o", tmp + ".o"}},
		{"joined -o/dev/null",
			[]string{"-Werror", "-c", "a.c", "-o/dev/null"},
			[]string{"-Werror", "-c", "a.c", "-o", tmp + ".o"}},
		{"assembly",
			[]string{"-S", "a.c", "-o", "/dev/null"},
			[]string{"-S", "a.c", "-o", tmp + ".s"}},
		{"stdin, unchanged from W1-C2",
			[]string{"-x", "c", "-c", "-", "-o", "/dev/null"},
			[]string{"-x", "c", "-c", "-", "-o", tmp + ".o"}},
		{"stdin under -###, unchanged from W1-C2",
			[]string{"-###", "-x", "c", "-c", "-"},
			[]string{"-###", "-x", "c", "-c", "-", "-o", tmp + ".o"}},
		{"a real output is left alone",
			[]string{"-c", "a.c", "-o", "a.o"},
			[]string{"-c", "a.c", "-o", "a.o"}},
	}
	for _, host := range []Platform{linuxAmd, linuxArm, darwinArm, darwinAmd} {
		for _, c := range cases {
			t.Run(host.OS+"-"+host.Arch+"/"+c.name, func(t *testing.T) {
				h := fakeHost(host, nil)
				h.TempOutput = func(suffix string) (string, error) { return tmp + suffix, nil }
				role, _ := ParseRole("cc")
				plan, err := Rewrite(role, c.in, h)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Args) < len(c.want) || !reflect.DeepEqual(plan.Args[len(plan.Args)-len(c.want):], c.want) {
					t.Errorf("got %q want suffix %q", plan.Args, c.want)
				}
			})
		}
	}
}

// TestRewriteMarch is kottera W1-A2's divergence row 21: zig 0.17.0 rejects
// gcc's and clang's -march=x86-64-v3 ("unknown CPU: 'x86'") and accepts
// x86_64_v3.
func TestRewriteMarch(t *testing.T) {
	cases := []struct {
		host     Platform
		role     string
		in, want string
	}{
		{linuxAmd, "cc", "-march=x86-64", "-march=x86_64"},
		{linuxAmd, "cc", "-march=x86-64-v2", "-march=x86_64_v2"},
		{linuxAmd, "c++", "-march=x86-64-v3", "-march=x86_64_v3"},
		{linuxAmd, "cc", "-march=x86-64-v4", "-march=x86_64_v4"},
		{darwinAmd, "cc", "-march=x86-64-v3", "-march=x86_64_v3"},
		{darwinArm, "x86_64-unknown-linux-gnu-cc", "-march=x86-64-v3", "-march=x86_64_v3"},
		{linuxArm, "x86_64-apple-darwin-cc", "-march=x86-64-v2", "-march=x86_64_v2"},
		// Any other value passes through.
		{linuxAmd, "cc", "-march=haswell", "-march=haswell"},
		{linuxAmd, "cc", "-march=native", "-march=native"},
		{linuxAmd, "cc", "-march=x86-64-v5", "-march=x86-64-v5"},
		{linuxAmd, "cc", "-march=x86_64_v3", "-march=x86_64_v3"},
		// Nothing is rewritten for a target that is not x86_64.
		{linuxArm, "cc", "-march=x86-64-v3", "-march=x86-64-v3"},
		{linuxAmd, "aarch64-unknown-linux-gnu-cc", "-march=x86-64-v3", "-march=x86-64-v3"},
		{darwinAmd, "aarch64-apple-darwin-cc", "-march=x86-64", "-march=x86-64"},
		{linuxArm, "cc", "-march=armv8.2-a", "-march=armv8.2-a"},
		// ar and ranlib are not compiles.
		{linuxAmd, "ar", "-march=x86-64-v3", "-march=x86-64-v3"},
	}
	for _, c := range cases {
		t.Run(c.host.OS+"-"+c.host.Arch+"/"+c.role+"/"+c.in, func(t *testing.T) {
			role, err := ParseRole(c.role)
			if err != nil {
				t.Fatal(err)
			}
			in := []string{"-O2", c.in, "-c", "a.c", "-o", "a.o"}
			plan, err := Rewrite(role, in, fakeHost(c.host, nil))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"-O2", c.want, "-c", "a.c", "-o", "a.o"}
			if len(plan.Args) < len(want) || !reflect.DeepEqual(plan.Args[len(plan.Args)-len(want):], want) {
				t.Errorf("got %q want suffix %q", plan.Args, want)
			}
		})
	}
}

func TestRewriteTargetUnset(t *testing.T) {
	cases := []struct {
		host  Platform
		role  string
		unset string
	}{
		{linuxAmd, "cc", EnvGlibc},
		{linuxAmd, "c++", EnvGlibc},
		{darwinArm, "cc", EnvMacOS},
		{darwinArm, "clang++", EnvMacOS},
		{darwinArm, "x86_64-unknown-linux-gnu-cc", EnvGlibc},
		{linuxAmd, "aarch64-apple-darwin-cc", EnvMacOS},
	}
	for _, c := range cases {
		t.Run(c.host.OS+"/"+c.role, func(t *testing.T) {
			role, _ := ParseRole(c.role)
			_, err := Rewrite(role, []string{"-c", "a.c"}, fakeHost(c.host, map[string]string{c.unset: ""}))
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("not refused: %v", err)
			}
			msg := r.Error()
			for _, want := range []string{c.unset + " is not set", "never falls back", "repair:", "export " + c.unset + "="} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal lacks %q:\n%s", want, msg)
				}
			}
		})
	}
	// The variable the role does not need may be unset.
	role, _ := ParseRole("cc")
	if _, err := Rewrite(role, nil, fakeHost(linuxAmd, map[string]string{EnvMacOS: ""})); err != nil {
		t.Errorf("linux cc needs only %s: %v", EnvGlibc, err)
	}
	if _, err := Rewrite(role, nil, fakeHost(darwinArm, map[string]string{EnvGlibc: ""})); err != nil {
		t.Errorf("darwin cc needs only %s: %v", EnvMacOS, err)
	}
	// ar and ranlib need neither.
	ar, _ := ParseRole("ar")
	if _, err := Rewrite(ar, nil, fakeHost(linuxAmd, map[string]string{EnvGlibc: "", EnvMacOS: ""})); err != nil {
		t.Errorf("ar needs no target: %v", err)
	}
}

func TestRewriteTargetMalformed(t *testing.T) {
	for _, bad := range []string{"2", "2.31 -I/usr/include", "latest", "2.31.1.1"} {
		role, _ := ParseRole("cc")
		_, err := Rewrite(role, nil, fakeHost(linuxAmd, map[string]string{EnvGlibc: bad}))
		if err == nil || !strings.Contains(err.Error(), "is not a version") {
			t.Errorf("%s=%q: %v", EnvGlibc, bad, err)
		}
	}
	for _, bad := range []string{"13.0 -F/Library/Frameworks", "latest", "13.0.1.1", "13.", "v13"} {
		role, _ := ParseRole("cc")
		_, err := Rewrite(role, nil, fakeHost(darwinArm, map[string]string{EnvMacOS: bad}))
		if err == nil || !strings.Contains(err.Error(), "is not a version") {
			t.Errorf("%s=%q: %v", EnvMacOS, bad, err)
		}
	}
	for _, good := range []string{"13", "13.0", "13.0.1"} {
		role, _ := ParseRole("cc")
		plan, err := Rewrite(role, nil, fakeHost(darwinArm, map[string]string{EnvMacOS: good}))
		if err != nil || plan.Target != "aarch64-macos."+good {
			t.Errorf("%s=%q: %v %q", EnvMacOS, good, err, plan.Target)
		}
	}
}

func TestRewriteUnsupportedHost(t *testing.T) {
	role, _ := ParseRole("cc")
	for _, p := range []Platform{{OS: "freebsd", Arch: "amd64"}, {OS: "linux", Arch: "riscv64"}} {
		if _, err := Rewrite(role, nil, fakeHost(p, nil)); err == nil {
			t.Errorf("%v: not refused", p)
		}
	}
	// A triple role still works there: its target is fixed.
	fixed, _ := ParseRole("x86_64-unknown-linux-gnu-cc")
	if _, err := Rewrite(fixed, nil, fakeHost(Platform{OS: "freebsd", Arch: "amd64"}, nil)); err != nil {
		t.Errorf("triple role on an unsupported host: %v", err)
	}
}

func TestParseRoleUnknown(t *testing.T) {
	for _, name := range []string{"zig-cc-shim", "cpp", "ld", "x86_64-unknown-linux-gnu-ar", "", "CC"} {
		_, err := ParseRole(name)
		var r *Refusal
		if !errors.As(err, &r) {
			t.Fatalf("%q not refused", name)
		}
		for _, role := range RoleNames() {
			if !strings.Contains(r.Why, role) {
				t.Errorf("%q: refusal does not name role %s", name, role)
			}
		}
	}
}

func TestRoleNames(t *testing.T) {
	want := []string{
		"aarch64-apple-darwin-c++", "aarch64-apple-darwin-cc",
		"aarch64-unknown-linux-gnu-c++", "aarch64-unknown-linux-gnu-cc",
		"ar", "c++", "cc", "clang", "clang++", "g++", "gcc", "ranlib",
		"x86_64-apple-darwin-c++", "x86_64-apple-darwin-cc",
		"x86_64-unknown-linux-gnu-c++", "x86_64-unknown-linux-gnu-cc",
	}
	if got := RoleNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// sameTargetTriples is every spelling of each role's own target that the
// Rust cc crate (or clang, or zig) passes as --target: kottera W1-A3 saw
// --target=x86_64-unknown-linux-gnu, and W1-A4 --target=arm64-apple-macosx,
// both rejected by zig 0.17.0. The version a triple carries is never the
// target's: the floor and the minimum come from CC_TOOLCHAIN_TARGET_*.
var sameTargetTriples = map[Platform][]string{
	linuxAmd: {"x86_64-unknown-linux-gnu", "x86_64-pc-linux-gnu", "x86_64-linux-gnu",
		"x86_64-linux-gnu.2.17", "x86_64-unknown-linux"},
	linuxArm: {"aarch64-unknown-linux-gnu", "aarch64-linux-gnu", "arm64-unknown-linux-gnu",
		"aarch64-linux-gnu.2.35"},
	darwinAmd: {"x86_64-apple-darwin", "x86_64-apple-macosx", "x86_64-apple-macosx13.0",
		"x86_64-apple-macosx26.4", "x86_64-apple-darwin23.1.0", "x86_64-macos", "x86_64-macos.14.0"},
	darwinArm: {"aarch64-apple-darwin", "arm64-apple-macosx", "arm64-apple-macosx13.0",
		"arm64-apple-macosx26.4", "arm64-apple-darwin", "aarch64-apple-darwin24.0.0",
		"aarch64-macos", "aarch64-macos.15.0"},
}

// TestRewriteCallerTargetSame is kottera W1-A3 and W1-A4: a caller's
// --target that restates the role's own target is dropped, in each of its
// three forms, so zig sees only the shim's -target.
func TestRewriteCallerTargetSame(t *testing.T) {
	type roleOn struct {
		host Platform
		role string
		// target is the platform the role targets.
		target Platform
	}
	var roles []roleOn
	for _, host := range []Platform{linuxAmd, linuxArm, darwinArm, darwinAmd} {
		roles = append(roles, roleOn{host, "cc", host}, roleOn{host, "c++", host})
		for triple, p := range Triples {
			roles = append(roles, roleOn{host, triple + "-cc", p}, roleOn{host, triple + "-c++", p})
		}
	}
	for _, r := range roles {
		for _, triple := range sameTargetTriples[r.target] {
			for _, form := range [][]string{{"--target=" + triple}, {"--target", triple}, {"-target", triple}} {
				name := r.host.OS + "-" + r.host.Arch + "/" + r.role + "/" + strings.Join(form, " ")
				t.Run(name, func(t *testing.T) {
					role, err := ParseRole(r.role)
					if err != nil {
						t.Fatal(err)
					}
					in := append(append([]string{"-O2"}, form...), "-c", "a.c", "-o", "a.o")
					plan, err := Rewrite(role, in, fakeHost(r.host, nil))
					if err != nil {
						t.Fatal(err)
					}
					want := []string{"-O2", "-c", "a.c", "-o", "a.o"}
					if len(plan.Args) < len(want) || !reflect.DeepEqual(plan.Args[len(plan.Args)-len(want):], want) {
						t.Errorf("got %q want suffix %q", plan.Args, want)
					}
					for i, a := range plan.Args {
						if a == "-target" && i != 1 || strings.HasPrefix(a, "--target") {
							t.Errorf("a caller's target reaches zig: %q", plan.Args)
						}
					}
					floor := "linux-gnu.2.31"
					if r.target.OS == "darwin" {
						floor = "macos.13.0"
					}
					if !strings.HasSuffix(plan.Target, floor) || plan.Args[2] != plan.Target {
						t.Errorf("target %q args %q: not the shim's own at %s", plan.Target, plan.Args, floor)
					}
				})
			}
		}
	}
}

// TestRewriteCallerTargetDifferent: a --target naming another architecture,
// OS or ABI is refused, never silently overridden, and the refusal names
// both targets and the role that builds the caller's.
func TestRewriteCallerTargetDifferent(t *testing.T) {
	cases := []struct {
		host   Platform
		role   string
		arg    []string
		useTo  string // the role the refusal names, or "" when none fits
		reason string
	}{
		{linuxAmd, "cc", []string{"--target=aarch64-unknown-linux-gnu"}, "aarch64-unknown-linux-gnu-cc", "architecture"},
		{linuxAmd, "c++", []string{"--target", "aarch64-unknown-linux-gnu"}, "aarch64-unknown-linux-gnu-c++", "architecture"},
		{darwinArm, "cc", []string{"--target=x86_64-apple-macosx"}, "x86_64-apple-darwin-cc", "architecture"},
		{darwinArm, "cc", []string{"-target", "aarch64-unknown-linux-gnu"}, "aarch64-unknown-linux-gnu-cc", "OS"},
		{linuxAmd, "x86_64-unknown-linux-gnu-cc", []string{"--target=x86_64-apple-darwin"}, "x86_64-apple-darwin-cc", "OS"},
		{darwinArm, "aarch64-apple-darwin-c++", []string{"--target=arm64-apple-ios"}, "", "OS"},
		{linuxAmd, "cc", []string{"--target=x86_64-unknown-linux-musl"}, "", "ABI"},
		{linuxAmd, "cc", []string{"--target=i686-unknown-linux-gnu"}, "", "architecture"},
		{darwinArm, "cc", []string{"--target=arm64e-apple-macosx"}, "", "architecture"},
		{linuxAmd, "cc", []string{"--target=x86_64-pc-windows-msvc"}, "", "OS"},
		{linuxAmd, "cc", []string{"--target="}, "", "not a triple"},
		// Every --target is checked, not only the first.
		{linuxAmd, "cc", []string{"--target=x86_64-unknown-linux-gnu", "-target", "aarch64-linux-gnu"}, "aarch64-unknown-linux-gnu-cc", "architecture"},
	}
	for _, c := range cases {
		t.Run(c.host.OS+"-"+c.host.Arch+"/"+c.role+"/"+strings.Join(c.arg, " "), func(t *testing.T) {
			role, _ := ParseRole(c.role)
			in := append(append([]string{}, c.arg...), "-c", "a.c")
			_, err := Rewrite(role, in, fakeHost(c.host, nil))
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("not refused: %v", err)
			}
			msg := r.Error()
			callerTarget := c.arg[len(c.arg)-1]
			callerTarget = strings.TrimPrefix(callerTarget, "--target=")
			_, own, _ := Target(role, c.host, fakeHost(c.host, nil).Getenv)
			for _, want := range []string{"zig-cc-shim (" + c.role + ")", callerTarget, own, c.reason, "repair:"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal lacks %q:\n%s", want, msg)
				}
			}
			if c.useTo != "" && !strings.Contains(r.Repair, "invoke "+c.useTo) {
				t.Errorf("refusal does not name the role %s:\n%s", c.useTo, msg)
			}
			if c.useTo == "" && !strings.Contains(r.Repair, "no role") {
				t.Errorf("refusal suggests a role for a target none builds:\n%s", msg)
			}
		})
	}
}

// TestRewriteCallerTargetValues: a --target that is another flag's value is
// not the caller's target, and ar and ranlib are left alone.
func TestRewriteCallerTargetValues(t *testing.T) {
	role, _ := ParseRole("cc")
	in := []string{"-Xclang", "--target=aarch64-unknown-linux-gnu", "-c", "a.c"}
	plan, err := Rewrite(role, in, fakeHost(linuxAmd, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Args[len(plan.Args)-len(in):]; !reflect.DeepEqual(got, in) {
		t.Errorf("got %q want suffix %q", plan.Args, in)
	}
	ar, _ := ParseRole("ar")
	in = []string{"--target=aarch64-unknown-linux-gnu", "rcs", "x.a"}
	if plan, err := Rewrite(ar, in, fakeHost(linuxAmd, nil)); err != nil || !reflect.DeepEqual(plan.Args[1:], in) {
		t.Errorf("ar: %q %v", plan.Args, err)
	}
}

// TestRewriteCallerSearchDirs pins kottera PLAN-2026-10-hermetic-c-toolchain
// ruling R2: on darwin a caller's absolute -L and -F that name a directory
// outside the SDK come out relative to the working directory, which zig does
// not re-root under --sysroot, and everything else comes out as it went in.
func TestRewriteCallerSearchDirs(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
		want []string // nil: the same as in
	}{
		{"joined -L under the working directory", []string{"-L/private/var/work/pkg/lib", "-lfoo"}, []string{"-Llib", "-lfoo"}},
		{"separate -L keeps its form", []string{"-L", "/private/var/work/pkg/lib", "-lfoo"}, []string{"-L", "lib", "-lfoo"}},
		{"the working directory itself", []string{"-L/private/var/work/pkg"}, []string{"-L."}},
		{"a sibling of the working directory", []string{"-L/private/var/work/other/lib"}, []string{"-L../other/lib"}},
		{"through a symlink", []string{"-L/var/work/other/lib"}, []string{"-L../other/lib"}},
		{"a Homebrew prefix", []string{"-L/opt/homebrew/lib", "-lssl"}, []string{"-L../../../../opt/homebrew/lib", "-lssl"}},
		{"joined -F", []string{"-F/opt/homebrew/Frameworks", "-framework", "Foo"}, []string{"-F../../../../opt/homebrew/Frameworks", "-framework", "Foo"}},
		{"separate -F", []string{"-F", "/opt/homebrew/Frameworks"}, []string{"-F", "../../../../opt/homebrew/Frameworks"}},
		{"several, in order", []string{"-L/opt/homebrew/lib", "-L.", "-L/private/var/work/pkg/lib"}, []string{"-L../../../../opt/homebrew/lib", "-L.", "-Llib"}},
		// Left to zig.
		{"relative -L", []string{"-L.", "-Llib", "-L", "lib"}, nil},
		{"relative -F", []string{"-FFrameworks", "-F", "Frameworks"}, nil},
		{"a directory the SDK also has", []string{"-L/usr/lib", "-F/System/Library/Frameworks"}, nil},
		{"inside the SDK", []string{"-L" + fakeSDK + "/usr/lib", "-F", fakeSDK + "/System/Library/Frameworks"}, nil},
		{"the SDK itself", []string{"-L" + fakeSDK}, nil},
		{"a directory that does not exist", []string{"-L/nonexistent/lib", "-F", "/nonexistent/Frameworks"}, nil},
		{"a -L that is another flag's value", []string{"-Xlinker", "-L/opt/homebrew/lib"}, nil},
		{"a trailing -L with no value", []string{"-L"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			role, _ := ParseRole("cc")
			plan, err := Rewrite(role, c.in, fakeHost(darwinArm, nil))
			if err != nil {
				t.Fatal(err)
			}
			want := c.want
			if want == nil {
				want = c.in
			}
			want = append(darwinPrefix("cc", "aarch64-macos.13.0"), want...)
			if !reflect.DeepEqual(plan.Args, want) {
				t.Errorf("args\n got %q\nwant %q", plan.Args, want)
			}
		})
	}
}

func TestRewriteCallerSearchDirsDarwinOnly(t *testing.T) {
	// No --sysroot on a Linux target, so zig re-roots nothing and the shim
	// leaves an absolute -L alone.
	in := []string{"-L/opt/homebrew/lib", "-F", "/opt/homebrew/Frameworks"}
	role, _ := ParseRole("x86_64-unknown-linux-gnu-cc")
	plan, err := Rewrite(role, in, fakeHost(darwinArm, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"cc", "-target", "x86_64-linux-gnu.2.31"}, in...)
	if !reflect.DeepEqual(plan.Args, want) {
		t.Errorf("args\n got %q\nwant %q", plan.Args, want)
	}
}

func TestRewriteCallerSearchDirsDash(t *testing.T) {
	// From /, /-odd is -odd, which a separate -L would read as a flag.
	h := fakeHost(darwinArm, nil)
	h.Getwd = func() (string, error) { return "/", nil }
	role, _ := ParseRole("cc")
	plan, err := Rewrite(role, []string{"-L", "/-odd", "-L/-odd"}, h)
	if err != nil {
		t.Fatal(err)
	}
	want := append(darwinPrefix("cc", "aarch64-macos.13.0"), "-L", "./-odd", "-L./-odd")
	if !reflect.DeepEqual(plan.Args, want) {
		t.Errorf("args\n got %q\nwant %q", plan.Args, want)
	}
}

func TestRewriteCallerSearchDirsGetwdFails(t *testing.T) {
	h := fakeHost(darwinArm, nil)
	h.Getwd = func() (string, error) { return "", errors.New("getwd: no such file or directory") }
	role, _ := ParseRole("cc")
	if _, err := Rewrite(role, []string{"-L/opt/homebrew/lib"}, h); err == nil || !strings.Contains(err.Error(), "-L/opt/homebrew/lib") {
		t.Errorf("err = %v, want one naming -L/opt/homebrew/lib", err)
	}
	// Nothing to keep, so the working directory is never asked for.
	if _, err := Rewrite(role, []string{"-L.", "-L/usr/lib"}, h); err != nil {
		t.Errorf("err = %v with no absolute directory to keep", err)
	}
}

// TestRewriteCallerSearchDirsSDKGuards pins each of keepCallerDir's guards
// with a case that comes out wrong without it: the SDK compared through its
// symlinks (the Command Line Tools' MacOSX.sdk is a symlink to the versioned
// SDK beside it), the caller's directory compared through its symlinks, and
// the SDK compared as a directory rather than a string prefix.
func TestRewriteCallerSearchDirsSDKGuards(t *testing.T) {
	const (
		linkSDK = "/clt/SDKs/MacOSX.sdk"
		realSDK = "/clt/SDKs/MacOSX15.sdk"
	)
	links := map[string]string{
		linkSDK:        realSDK,
		"/opt/sdkpath": fakeSDK + "/usr",
	}
	dirs := map[string]bool{
		realSDK: true, realSDK + "/usr/lib": true, realSDK + "/extra/lib": true,
		fakeSDK + "-extra/lib": true,
	}
	for k := range fakeDirs {
		dirs[k] = true
	}
	eval := func(p string) (string, error) {
		p = filepath.Clean(p)
		for link, real := range links {
			if rest, ok := strings.CutPrefix(p, link); ok && (rest == "" || rest[0] == '/') {
				p = real + rest
			}
		}
		if !dirs[p] {
			return "", os.ErrNotExist
		}
		return p, nil
	}
	for _, c := range []struct {
		name, sdk string
		in, want  []string // want nil: the same as in
	}{
		{"inside the SDK the SDK path resolves to", linkSDK, []string{"-L" + realSDK + "/extra/lib"}, nil},
		{"a symlink into the SDK", fakeSDK, []string{"-L/opt/sdkpath/lib", "-L", "/opt/sdkpath/lib"}, nil},
		{"a sibling whose name extends the SDK's", fakeSDK, []string{"-L" + fakeSDK + "-extra/lib"}, []string{"-L../../../../sdk/MacOSX.sdk-extra/lib"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := fakeHost(darwinArm, map[string]string{"SDKROOT": c.sdk})
			h.SDK.Dir = func(d string) error {
				if _, err := eval(d); err != nil {
					return fs.ErrNotExist
				}
				return nil
			}
			h.EvalSymlinks = eval
			h.IsDir = func(d string) bool { _, err := eval(d); return err == nil }
			role, _ := ParseRole("cc")
			plan, err := Rewrite(role, c.in, h)
			if err != nil {
				t.Fatal(err)
			}
			want := c.want
			if want == nil {
				want = c.in
			}
			if got := plan.Args[len(plan.Args)-len(c.in):]; !reflect.DeepEqual(got, want) {
				t.Errorf("args\n got %q\nwant %q", plan.Args, want)
			}
		})
	}
}

// TestResponseFileArgs holds zig 0.17.0's own cases for the reader of a
// `zig cc` @file (lib/std/process/Args.zig, "response file arg parsing"),
// and a few of the shapes a build system writes.
func TestResponseFileArgs(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"a b\nc d\\", []string{"a", "b", "c", "d\\"}},
		{"a b c d\\", []string{"a", "b", "c", "d\\"}},
		{"j\n k l # this is a comment \\\\ \\\\\\ \\\\\\\\ \"none\" \"\\\\\" \"\\\\\\\"\n \"m\" #another comment\n", []string{"j", "k", "l", "m"}},
		{" \"\" q \"\"\n \"r s # t\" \"u\\\" v\" #another comment\n", []string{"", "q", "", "r s # t", "u\" v"}},
		{" -l\"advapi32\" a# b#c d#\ne\\\\\\", []string{"-ladvapi32", "a#", "b#c", "d#", "e\\\\\\"}},
		{" 'foo' \"bar\"", []string{"foo", "bar"}},
		// Build systems' shapes.
		{"", nil},
		{"-L/a/b\r\n-lfoo\r\n", []string{"-L/a/b", "-lfoo"}},
		{"\"-L/with space/lib\" -L '/x y'", []string{"-L/with space/lib", "-L", "/x y"}},
		{"-DX=\\\"v\\\" C:\\dir\\", []string{"-DX=\"v\"", "C:\\dir\\"}},
	} {
		if got := ResponseFileArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ResponseFileArgs(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestRewriteCallerSearchDirsResponseFile is W1-F2.b, ruling R2 for a -L or
// -F that arrives in an @file: the @file is passed as the caller wrote it,
// and each absolute directory in it that zig would lose follows it, respelled
// as a direct one is.
func TestRewriteCallerSearchDirsResponseFile(t *testing.T) {
	files := map[string]string{
		"link.rsp": "-L/opt/homebrew/lib -lssl -lssl\n" +
			"-F /opt/homebrew/Frameworks -framework Foo\n" +
			"# -L/private/var/work/other/lib\n" +
			"-L/usr/lib -Lrel -L/nonexistent/lib -L" + fakeSDK + "/usr/lib\n" +
			"-Xlinker -L/private/var/work/other/lib\n" +
			"'-L/private/var/work/pkg/lib' -o out\n",
		"plain.rsp": "-c a.c -o a.o\n",
	}
	h := fakeHost(darwinArm, nil)
	read := h.ReadFile
	var asked []string
	h.ReadFile = func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			asked = append(asked, p)
			return []byte(c), nil
		}
		return read(p)
	}
	for _, c := range []struct {
		name     string
		in, want []string
	}{
		{"kept directories follow the @file, in its order and the caller's form",
			[]string{"main.o", "@link.rsp", "-o", "main"},
			[]string{"main.o", "@link.rsp", "-L../../../../opt/homebrew/lib", "-F", "../../../../opt/homebrew/Frameworks", "-Llib", "-o", "main"}},
		{"composes with a direct -L", []string{"-L/opt/homebrew/lib", "@link.rsp"},
			[]string{"-L../../../../opt/homebrew/lib", "@link.rsp", "-L../../../../opt/homebrew/lib", "-F", "../../../../opt/homebrew/Frameworks", "-Llib"}},
		{"an @file with nothing to keep", []string{"@plain.rsp"}, nil},
		{"an @file the shim cannot read is left to zig", []string{"@missing.rsp"}, nil},
		{"an @file as another flag's value is not read", []string{"-o", "@link.rsp", "-Xlinker", "@link.rsp"}, nil},
		{"a lone @", []string{"@"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			role, _ := ParseRole("cc")
			plan, err := Rewrite(role, c.in, h)
			if err != nil {
				t.Fatal(err)
			}
			want := c.want
			if want == nil {
				want = c.in
			}
			want = append(darwinPrefix("cc", "aarch64-macos.13.0"), want...)
			if !reflect.DeepEqual(plan.Args, want) {
				t.Errorf("args\n got %q\nwant %q", plan.Args, want)
			}
		})
	}

	// A Linux target re-roots nothing, so its @file is not read.
	asked = nil
	role, _ := ParseRole("x86_64-unknown-linux-gnu-cc")
	plan, err := Rewrite(role, []string{"@link.rsp"}, h)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"cc", "-target", "x86_64-linux-gnu.2.31", "@link.rsp"}; !reflect.DeepEqual(plan.Args, want) || len(asked) != 0 {
		t.Errorf("linux: args %q (read %q), want %q and no @file read", plan.Args, asked, want)
	}

	h.Getwd = func() (string, error) { return "", errors.New("getwd: no such file or directory") }
	role, _ = ParseRole("cc")
	if _, err := Rewrite(role, []string{"@link.rsp"}, h); err == nil || !strings.Contains(err.Error(), "-L/opt/homebrew/lib") {
		t.Errorf("err = %v, want one naming -L/opt/homebrew/lib", err)
	}
}
