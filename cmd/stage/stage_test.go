package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kottera/zig-cc-shim/internal/shim"
)

// TestStagedStore stages the shim, puts a fake zig at zig/zig, and runs the
// role entries for real: path resolution, role dispatch by name and
// syscall.Exec are proved here, with no zig on the host.
func TestStagedStore(t *testing.T) {
	dest := t.TempDir()
	if err := Stage(dest, runtime.GOOS, runtime.GOARCH, io.Discard); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dest, "bin")
	cc := filepath.Join(bin, "cc")

	// Before zig is unpacked, a role refuses naming the path it looked at.
	_, stderr, code := run(t, cc, nil, "-c", "a.c")
	if code != 1 || !strings.Contains(stderr, filepath.Join("zig", "zig")) || !strings.Contains(stderr, "refused") {
		t.Errorf("no zig: exit %d, %s", code, stderr)
	}

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command(goTool, "build", "-o", filepath.Join(dest, "zig", "zig"), "./testdata/fakezig")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fakezig: %v\n%s", err, out)
	}

	t.Run("every role is a hard link to one build", func(t *testing.T) {
		first, err := os.Stat(cc)
		if err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(bin)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
			fi, err := os.Lstat(filepath.Join(bin, e.Name()))
			if err != nil || !fi.Mode().IsRegular() || !os.SameFile(first, fi) {
				t.Errorf("%s is not a hard link to bin/cc", e.Name())
			}
		}
		if !slices.Equal(names, shim.RoleNames()) {
			t.Errorf("bin/ holds %q, want %q", names, shim.RoleNames())
		}
	})

	t.Run("ar passes through and the exit status is zig's", func(t *testing.T) {
		args, _, code := runZig(t, filepath.Join(bin, "ar"), []string{"FAKEZIG_EXIT=7"}, "rcs", "x.a", "a.o")
		if code != 7 {
			t.Errorf("exit %d, want zig's 7", code)
		}
		if want := []string{"ar", "rcs", "x.a", "a.o"}; !slices.Equal(args, want) {
			t.Errorf("zig got %q want %q", args, want)
		}
	})

	sdk := t.TempDir()
	env := []string{shim.EnvGlibc + "=2.17", shim.EnvMacOS + "=13.0", "SDKROOT=" + sdk}

	t.Run("cc supplies the target", func(t *testing.T) {
		args, stderr, code := runZig(t, cc, env, "-c", "a.c", "-o", "a.o")
		if runtime.GOOS == "linux" && strings.Contains(stderr, "glibc version") {
			t.Skipf("this Linux host's glibc cannot be read: %s", stderr)
		}
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		want := []string{"cc", "-target"}
		switch runtime.GOOS {
		case "darwin":
			want = append(want, map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]+"-macos.13.0")
			want = append(want, shim.DarwinFlags(sdk)...)
		case "linux":
			want = append(want, map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]+"-linux-gnu.2.17")
		}
		want = append(want, "-c", "a.c", "-o", "a.o")
		if !slices.Equal(args, want) {
			t.Errorf("zig got %q want %q", args, want)
		}
	})

	t.Run("a stdin probe writes under TMPDIR, not ./-.o", func(t *testing.T) {
		tmp := t.TempDir()
		e := append(slices.Clone(env), "TMPDIR="+tmp)
		args, stderr, code := runZig(t, filepath.Join(bin, "x86_64-unknown-linux-gnu-cc"), e, "-x", "c", "-c", "-", "-o", "/dev/null")
		if runtime.GOOS == "linux" && strings.Contains(stderr, "glibc version") {
			t.Skipf("this Linux host's glibc cannot be read: %s", stderr)
		}
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		out := args[len(args)-1]
		if args[len(args)-2] != "-o" || !strings.HasPrefix(out, tmp) || !strings.HasSuffix(out, ".o") {
			t.Errorf("zig got %q", args)
		}
	})

	t.Run("--tera-describe", func(t *testing.T) {
		stdout, stderr, code := run(t, cc, env, shim.DescribeFlag)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		zig, _ := filepath.EvalSymlinks(filepath.Join(dest, "zig", "zig"))
		for _, want := range []string{"zig-cc-shim-describe: 1\n", "role: cc\n", "zig: " + zig + "\n", "zig-version: 0.0.0-fake\n", "target: "} {
			if !strings.Contains(stdout, want) {
				t.Errorf("describe lacks %q:\n%s", want, stdout)
			}
		}
		if runtime.GOOS == "darwin" && !strings.Contains(stdout, "sdk: "+sdk+"\n") {
			t.Errorf("describe lacks the SDK:\n%s", stdout)
		}
	})

	t.Run("a symlink elsewhere finds the store's zig", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "ranlib")
		if err := os.Symlink(filepath.Join(bin, "ranlib"), link); err != nil {
			t.Fatal(err)
		}
		args, stderr, code := runZig(t, link, nil, "x.a")
		if code != 0 || !slices.Equal(args, []string{"ranlib", "x.a"}) {
			t.Errorf("exit %d, zig got %q: %s", code, args, stderr)
		}
	})

	t.Run("an unknown name is refused, naming the roles", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "cpp")
		if err := os.Symlink(cc, link); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := run(t, link, nil, "-E", "a.c")
		if code != 1 || stdout != "" || !strings.Contains(stderr, `"cpp"`) || !strings.Contains(stderr, "clang++") {
			t.Errorf("exit %d, stdout %q, stderr %s", code, stdout, stderr)
		}
	})

	t.Run("staging again replaces the entries", func(t *testing.T) {
		if err := Stage(dest, runtime.GOOS, runtime.GOARCH, io.Discard); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(bin, ".zig-cc-shim.build")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the build was left behind: %v", err)
		}
		if _, _, code := run(t, filepath.Join(bin, "ar"), nil, "t", "x.a"); code != 0 {
			t.Errorf("exit %d after restaging", code)
		}
	})
}

// run executes path with the inherited environment minus the target
// variables and SDKROOT, plus env.
func run(t *testing.T, path string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(path, args...)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != shim.EnvGlibc && k != shim.EnvMacOS && k != "SDKROOT" && k != "FAKEZIG_EXIT" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("running %s: %v", path, err)
	}
	return o.String(), e.String(), code
}

// runZig runs a role and decodes the arguments the fake zig received.
func runZig(t *testing.T, path string, env []string, args ...string) ([]string, string, int) {
	t.Helper()
	stdout, stderr, code := run(t, path, env, args...)
	var got []string
	if stdout != "" {
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("fake zig output %q: %v (stderr %s)", stdout, err, stderr)
		}
	}
	return got, stderr, code
}
