package shim

import (
	"errors"
	"os"
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
// SDK found by xcrun, and glibc 2.39 readable from the loader.
func fakeHost(p Platform, env map[string]string) Host {
	vars := map[string]string{EnvGlibc: "2.31", EnvMacOS: "13.0"}
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
			IsDir:  func(d string) bool { return d == fakeSDK },
			Xcrun:  func() (string, error) { return fakeSDK, nil },
		},
		TempOutput: func(suffix string) (string, error) { return "/tmpdir/zig-cc-shim-out-1" + suffix, nil },
	}
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
