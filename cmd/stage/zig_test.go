package main

import (
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kottera/zig-cc-shim/internal/barehost"
	"github.com/kottera/zig-cc-shim/internal/shim"
)

// The integration tests below run the staged shim in front of a real zig. They
// run when EnvZig names one and otherwise skip naming it; on a host that
// declares itself bare (KOTTERA_TEST_BARE_HOST, kottera ADR-151 D3) a missing
// zig fails them instead. CI sets neither, so there they skip with that reason
// and nothing downloads zig.
const (
	// EnvZig names the zig executable of an unpacked zig release: the zig
	// file beside its lib/.
	EnvZig = "ZIG_CC_SHIM_TEST_ZIG"
	// EnvKottera names a kottera/kottera checkout, for the darwin proof that
	// `go test -race ./pkg/toolchain` passes through the shim.
	EnvKottera = "ZIG_CC_SHIM_TEST_KOTTERA"
)

// Defaults for the target variables when the environment does not set them:
// the cc definition's seeded values (kottera ADR-155 D2).
const (
	defaultGlibc = "2.31"
	defaultMacOS = "13.0"
)

// zigStore is the shim staged beside a real zig.
type zigStore struct {
	zig string // the zig the shim runs, through the store
	bin string // <store>/bin, the role entries
}

func (s zigStore) role(name string) string { return filepath.Join(s.bin, name) }

// requireZigStore reads EnvZig and stages the shim beside that zig with
// Stage, which is what `go run ./cmd/stage` runs. The store is a temporary
// directory whose zig/ is a symlink to the release's own directory, so the
// shim's <resolved self>/../zig/zig is that zig and nothing is written into
// the release.
func requireZigStore(t *testing.T) zigStore {
	t.Helper()
	zig := os.Getenv(EnvZig)
	if zig == "" {
		barehost.Missing(t, "%s is not set, so there is no zig to run the shim in front of; fix: unpack a zig "+
			"release and set %s to its zig executable (see the README)", EnvZig, EnvZig)
	}
	real, err := filepath.EvalSymlinks(zig)
	if err != nil {
		t.Fatalf("%s=%s: %v", EnvZig, zig, err)
	}
	if filepath.Base(real) != "zig" {
		t.Fatalf("%s=%s resolves to %s; the shim runs <store>/zig/zig, so the executable must be named zig",
			EnvZig, zig, real)
	}
	if fi, err := os.Stat(filepath.Join(filepath.Dir(real), "lib")); err != nil || !fi.IsDir() {
		t.Fatalf("%s=%s: no lib/ beside it; name the zig of an unpacked release", EnvZig, zig)
	}
	for _, v := range []struct{ name, value string }{{shim.EnvGlibc, defaultGlibc}, {shim.EnvMacOS, defaultMacOS}} {
		if os.Getenv(v.name) == "" {
			t.Setenv(v.name, v.value)
		}
	}
	dest := t.TempDir()
	if err := os.Symlink(filepath.Dir(real), filepath.Join(dest, "zig")); err != nil {
		t.Fatal(err)
	}
	if err := Stage(dest, runtime.GOOS, runtime.GOARCH, io.Discard); err != nil {
		t.Fatal(err)
	}
	return zigStore{zig: filepath.Join(dest, "zig", "zig"), bin: filepath.Join(dest, "bin")}
}

// hostTarget is the zig -target a host role uses on this host.
func hostTarget(t *testing.T) string {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	switch runtime.GOOS {
	case "darwin":
		return arch + "-macos." + os.Getenv(shim.EnvMacOS)
	case "linux":
		return arch + "-linux-gnu." + os.Getenv(shim.EnvGlibc)
	}
	t.Skipf("the shim has no host target on %s", runtime.GOOS)
	return ""
}

// command runs name in dir with extra environment, returning its combined
// output and failing the test if it does not exit 0.
func command(t *testing.T, dir string, env []string, stdin string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// requireEmpty fails if dir holds anything.
func requireEmpty(t *testing.T, dir, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s left %s in its working directory", what, e.Name())
	}
}

func TestZigEndToEnd(t *testing.T) {
	store := requireZigStore(t)
	target := hostTarget(t)

	t.Run("cc compiles and links a C program that runs", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "hello.c", "#include <stdio.h>\nint main(void) { printf(\"hello from cc\\n\"); return 0; }\n")
		command(t, dir, nil, "", store.role("cc"), "hello.c", "-o", "hello")
		if out := command(t, dir, nil, "", filepath.Join(dir, "hello")); out != "hello from cc\n" {
			t.Errorf("hello printed %q", out)
		}
	})

	t.Run("c++ compiles and links a C++ program that runs", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "hello.cc", "#include <iostream>\n#include <string>\n"+
			"int main() { std::string s = \"hello from c++\"; std::cout << s << std::endl; return 0; }\n")
		command(t, dir, nil, "", store.role("c++"), "hello.cc", "-o", "hello")
		if out := command(t, dir, nil, "", filepath.Join(dir, "hello")); out != "hello from c++\n" {
			t.Errorf("hello printed %q", out)
		}
	})

	t.Run("ar builds an archive a program links", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "add.c", "int add(int a, int b) { return a + b; }\n")
		writeFile(t, dir, "main.c", "#include <stdio.h>\nint add(int, int);\n"+
			"int main(void) { printf(\"%d\\n\", add(40, 2)); return 0; }\n")
		command(t, dir, nil, "", store.role("cc"), "-c", "add.c", "-o", "add.o")
		command(t, dir, nil, "", store.role("ar"), "rcs", "libadd.a", "add.o")
		if out := command(t, dir, nil, "", store.role("ar"), "t", "libadd.a"); strings.TrimSpace(out) != "add.o" {
			t.Errorf("ar t libadd.a listed %q, want add.o", out)
		}
		command(t, dir, nil, "", store.role("cc"), "main.c", "-L.", "-ladd", "-o", "main")
		if out := command(t, dir, nil, "", filepath.Join(dir, "main")); out != "42\n" {
			t.Errorf("main printed %q", out)
		}
	})

	// zig writes ./-.o for both of Go's stdin probes (kottera ADR-155, Where
	// zig cc broke, item 5): the flag probe `-c - -o /dev/null` and the
	// compiler identification `-### -x c -c -`. Each runs in an empty
	// directory, which must still be empty after.
	for _, probe := range [][]string{
		{"-Werror", "-x", "c", "-c", "-", "-o", "/dev/null"},
		{"-###", "-x", "c", "-c", "-"},
	} {
		name := "cc " + strings.Join(probe, " ") + " leaves no ./-.o"
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			command(t, dir, nil, "int x;\n", store.role("cc"), probe...)
			requireEmpty(t, dir, name)
		})
	}

	t.Run("--tera-describe reports the zig version and the target", func(t *testing.T) {
		version := strings.TrimSpace(command(t, "", nil, "", store.zig, "version"))
		got := describe(t, store.role("cc"))
		if got["zig-version"] != version {
			t.Errorf("zig-version: %q, want `zig version`'s %q", got["zig-version"], version)
		}
		if got["target"] != target {
			t.Errorf("target: %q, want %q", got["target"], target)
		}
		if got["zig"] != store.zig {
			t.Errorf("zig: %q, want %q", got["zig"], store.zig)
		}
		t.Logf("MEASURE zig-version %s, target %s, sdk %q", got["zig-version"], got["target"], got["sdk"])
	})
}

// describe runs `<role> --tera-describe` and returns its keys.
func describe(t *testing.T, role string) map[string]string {
	t.Helper()
	keys := map[string]string{}
	for _, line := range strings.Split(command(t, "", nil, "", role, "--tera-describe"), "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok {
			keys[k] = v
		}
	}
	return keys
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestZigKotteraRaceOnDarwin is ADR-155's headline darwin proof (kottera
// PLAN-2026-10-hermetic-c-toolchain section 4): `go test -race
// ./pkg/toolchain` of a kottera checkout passes with CC and CXX at the shim.
// The control arm runs first and pins the property false: bare `zig cc`, with
// the same target and SDK flags but no de-duplication, links CoreFoundation
// twice and the test binary aborts in dyld.
func TestZigKotteraRaceOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("the duplicated CoreFoundation is a darwin failure; this host is %s", runtime.GOOS)
	}
	store := requireZigStore(t)
	checkout := os.Getenv(EnvKottera)
	if checkout == "" {
		barehost.Missing(t, "%s is not set, so there is no kottera checkout to build; fix: clone kottera/kottera "+
			"and set %s to it", EnvKottera, EnvKottera)
	}
	if mod, err := os.ReadFile(filepath.Join(checkout, "go.mod")); err != nil ||
		!bytes.HasPrefix(mod, []byte("module github.com/kottera/kottera\n")) {
		t.Fatalf("%s=%s is not a kottera/kottera checkout (go.mod: %v)", EnvKottera, checkout, err)
	}
	// Go's compiler identification runs in the package's directory, and bare
	// zig answers it by writing -.o in the checkout's root.
	litter := filepath.Join(checkout, "-.o")
	if _, err := os.Lstat(litter); err == nil {
		t.Fatalf("%s already exists; remove it so this test can tell whether a build wrote it", litter)
	}

	d := describe(t, store.role("cc"))
	sdk := d["sdk"]
	if sdk == "" || strings.ContainsAny(sdk, " \t'\"") {
		t.Fatalf("the shim's SDK %q cannot be passed to bare zig in CC, which go splits on spaces", sdk)
	}
	bareFlags := strings.Join(append([]string{"-target", d["target"]}, shim.DarwinFlags(sdk)...), " ")

	goTest := func(t *testing.T, cc, cxx string) (string, error) {
		t.Helper()
		goTool, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(goTool, "test", "-race", "-count=1", "./pkg/toolchain")
		cmd.Dir = checkout
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "CC="+cc, "CXX="+cxx)
		out, err := cmd.CombinedOutput()
		t.Logf("MEASURE CGO_ENABLED=1 CC=%q CXX=%q go test -race -count=1 ./pkg/toolchain: %v\n%s", cc, cxx, err, tail(out, 20))
		return string(out), err
	}

	t.Run("control: bare zig cc aborts on the duplicated CoreFoundation", func(t *testing.T) {
		defer os.Remove(litter) // the control's own -.o, which the shim must not write
		out, err := goTest(t, store.zig+" cc "+bareFlags, store.zig+" c++ "+bareFlags)
		if err == nil {
			t.Fatal("go test -race passed through bare zig cc, so the shim's de-duplication is no longer what " +
				"makes it pass and the shim arm proves nothing")
		}
		if !strings.Contains(out, "duplicate linked dylib") || !strings.Contains(out, "CoreFoundation") {
			t.Fatalf("go test -race failed through bare zig cc, but not with dyld's duplicate linked dylib "+
				"CoreFoundation, so this is not the failure the shim fixes:\n%s", out)
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("shim: go test -race ./pkg/toolchain passes", func(t *testing.T) {
		if out, err := goTest(t, store.role("cc"), store.role("c++")); err != nil {
			t.Fatalf("go test -race through the shim: %v\n%s", err, out)
		}
		if _, err := os.Lstat(litter); err == nil {
			os.Remove(litter)
			t.Errorf("the build through the shim wrote %s", litter)
		}
	})
}

// tail returns the last n lines of out.
func tail(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

const cgoRaceProgram = `package main

/*
#include <stdlib.h>
static int add(int a, int b) { return a + b; }
*/
import "C"

import (
	"fmt"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sum := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := int(C.add(C.int(10), C.int(i)))
			mu.Lock()
			sum += v
			mu.Unlock()
		}()
	}
	wg.Wait()
	fmt.Println("cgo says", sum)
}
`

// TestZigCgoRaceGlibcCeiling builds a cgo program with -race through the
// shim and asserts that the highest GLIBC_ symbol version it requires is at or
// below CC_TOOLCHAIN_TARGET_GLIBC (kottera ADR-155 D2). On Linux it builds
// with the host role and runs the program. Elsewhere it cross-builds for
// linux/amd64, the fleet's Linux host, with the triple role and only reads the
// ELF: nothing is run on Linux there.
//
// It is not cross-built for linux/arm64: with zig 0.17.0 that link fails,
// because Go's race runtime for linux/arm64 calls libgcc's outline atomics
// (__aarch64_ldadd8_sync and others) and zig does not supply them. On a
// linux/arm64 host this test builds natively and meets that failure.
func TestZigCgoRaceGlibcCeiling(t *testing.T) {
	store := requireZigStore(t)
	env := []string{"CGO_ENABLED=1", "GOOS=linux", "CC=" + store.role("cc"), "CXX=" + store.role("c++")}
	if runtime.GOOS != "linux" {
		env = []string{"CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64",
			"CC=" + store.role("x86_64-unknown-linux-gnu-cc"), "CXX=" + store.role("x86_64-unknown-linux-gnu-c++")}
	}

	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module cgorace\n\ngo 1.21\n")
	writeFile(t, dir, "main.go", cgoRaceProgram)
	command(t, dir, env, "", "go", "build", "-race", "-o", "prog", ".")
	prog := filepath.Join(dir, "prog")

	if runtime.GOOS == "linux" {
		if out := command(t, dir, nil, "", prog); out != "cgo says 46\n" {
			t.Errorf("prog printed %q", out)
		}
	} else {
		t.Logf("cross-built for linux/amd64 on %s/%s: the program is not run here", runtime.GOOS, runtime.GOARCH)
	}

	floor := os.Getenv(shim.EnvGlibc)
	highest, err := highestGlibc(prog)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURE highest GLIBC_ required: %s (floor %s)", highest, floor)
	if highest == "" {
		t.Fatal("the program requires no GLIBC_ version; a cgo -race build links glibc, so the reading is wrong")
	}
	if compareVersions(highest, floor) > 0 {
		t.Errorf("the program requires GLIBC_%s, above %s=%s", highest, shim.EnvGlibc, floor)
	}
}

// highestGlibc returns the highest GLIBC_<version> the ELF file at path
// requires, without the prefix, or "" when it requires none.
func highestGlibc(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	needs, err := f.DynamicVersionNeeds()
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	highest := ""
	for _, need := range needs {
		for _, dep := range need.Needs {
			v, ok := strings.CutPrefix(dep.Dep, "GLIBC_")
			if !ok || !isVersion(v) {
				continue // GLIBC_PRIVATE and other libraries' versions
			}
			if highest == "" || compareVersions(v, highest) > 0 {
				highest = v
			}
		}
	}
	return highest, nil
}

func isVersion(v string) bool {
	for _, p := range strings.Split(v, ".") {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// compareVersions compares dotted numeric versions: -1, 0 or 1.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2.31", "2.31", 0}, {"2.17", "2.31", -1}, {"2.34", "2.31", 1},
		{"2.3.4", "2.3", 1}, {"2.3", "2.3.0", 0}, {"2.9", "2.10", -1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
