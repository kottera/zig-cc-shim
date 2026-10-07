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
	"time"

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
		// The shim reports the zig it resolved through symlinks, and the
		// store may sit under one ($TMPDIR is under /var -> /private/var on
		// darwin), so both sides are compared resolved.
		if gotZig, wantZig := resolved(t, got["zig"]), resolved(t, store.zig); gotZig != wantZig {
			t.Errorf("zig: %q (resolved %q), want %q (resolved %q)", got["zig"], gotZig, store.zig, wantZig)
		}
		t.Logf("MEASURE zig-version %s, target %s, sdk %q", got["zig-version"], got["target"], got["sdk"])
	})
}

// TestZigDevNullOutputsBounded is W1-F2.a: the outputs the shim gives zig in
// place of -o /dev/null do not pile up under $TMPDIR. zig writes each one and
// exits, and the next compile to /dev/null removes those whose process has
// exited once they are past shim.TempOutputGrace. The test runs several such
// compiles, ages their outputs past the grace period, and runs one more,
// which must leave its own output alone; the working directory holds only
// the source throughout.
func TestZigDevNullOutputsBounded(t *testing.T) {
	store := requireZigStore(t)
	tmp, dir := t.TempDir(), t.TempDir()
	env := []string{"TMPDIR=" + tmp}
	writeFile(t, dir, "a.c", "int a;\n")
	outputs := func() []string {
		t.Helper()
		matches, err := filepath.Glob(filepath.Join(tmp, shim.TempOutputPrefix+"*"))
		if err != nil {
			t.Fatal(err)
		}
		return matches
	}
	compile := func() {
		t.Helper()
		command(t, dir, env, "", store.role("cc"), "-c", "a.c", "-o", "/dev/null")
		command(t, dir, env, "int x;\n", store.role("cc"), "-Werror", "-x", "c", "-c", "-", "-o", "/dev/null")
	}
	const rounds = 3
	for range rounds {
		compile()
	}
	if got := outputs(); len(got) != 2*rounds {
		t.Fatalf("%d compiles inside the grace period left %d outputs, want one each: %q", 2*rounds, len(got), got)
	}
	aged := time.Now().Add(-shim.TempOutputGrace - time.Minute)
	for _, p := range outputs() {
		if err := os.Chtimes(p, aged, aged); err != nil {
			t.Fatal(err)
		}
	}
	command(t, dir, env, "", store.role("cc"), "-c", "a.c", "-o", "/dev/null")
	got := outputs()
	if len(got) != 1 {
		t.Fatalf("a compile after %d aged outputs left %d: %q, want only its own", 2*rounds, len(got), got)
	}
	if fi, err := os.Stat(got[0]); err != nil || fi.Size() == 0 {
		t.Errorf("zig did not write the output the shim named, %s: %v", got[0], err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Errorf("the compiles wrote into their working directory: %v %v", entries, err)
	}
}

// resolved is path with every symlink resolved, or the test fails.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving %q: %v", path, err)
	}
	return real
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

// TestZigCallerSearchDirsOnDarwin is kottera PLAN-2026-10-hermetic-c-toolchain
// ruling R2: on darwin a caller's absolute -L and -F that name a directory
// outside the SDK reach the link. A static library and a framework are built
// in a temporary directory, which is outside the SDK, and a program in another
// directory links each by its absolute path, in both the joined and the
// separate spelling, and runs. The control arm runs first and pins the
// property false: bare zig cc, given the same target and SDK flags, does not
// find the library.
func TestZigCallerSearchDirsOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("zig re-roots a caller's -L under the SDK only with --sysroot, which the shim passes on darwin; this host is %s", runtime.GOOS)
	}
	store := requireZigStore(t)
	d := describe(t, store.role("cc"))
	lib := t.TempDir()
	if strings.HasPrefix(resolved(t, lib), resolved(t, d["sdk"])) {
		t.Fatalf("the temporary directory %s is inside the SDK %s", lib, d["sdk"])
	}

	writeFile(t, lib, "add.c", "int add(int a, int b) { return a + b; }\n")
	command(t, lib, nil, "", store.role("cc"), "-c", "add.c", "-o", "add.o")
	command(t, lib, nil, "", store.role("ar"), "rcs", "libadd.a", "add.o")

	fw := filepath.Join(lib, "Answer.framework")
	if err := os.MkdirAll(filepath.Join(fw, "Headers"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fw, "Headers"), "answer.h", "int answer(void);\n")
	writeFile(t, lib, "answer.c", "int answer(void) { return 42; }\n")
	command(t, lib, nil, "", store.role("cc"), "-shared", "answer.c",
		"-Wl,-install_name,"+filepath.Join(fw, "Answer"), "-o", filepath.Join(fw, "Answer"))

	run := func(t *testing.T, dir string) {
		t.Helper()
		if out := command(t, dir, nil, "", filepath.Join(dir, "main")); out != "42\n" {
			t.Errorf("main printed %q", out)
		}
	}
	addMain := "#include <stdio.h>\nint add(int, int);\nint main(void) { printf(\"%d\\n\", add(40, 2)); return 0; }\n"

	t.Run("control: bare zig cc drops the absolute -L", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "main.c", addMain)
		args := append([]string{"cc", "-target", d["target"]}, shim.DarwinFlags(d["sdk"])...)
		args = append(args, "main.c", "-L"+lib, "-ladd", "-o", "main")
		cmd := exec.Command(store.zig, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		t.Logf("MEASURE zig %s: %v\n%s", strings.Join(args, " "), err, out)
		if err == nil {
			t.Fatal("bare zig cc found libadd.a through an absolute -L under --sysroot, so the shim's rewriting " +
				"is no longer what makes the shim arms pass and they prove nothing")
		}
		if !strings.Contains(string(out), "add") {
			t.Fatalf("bare zig cc failed, but its error does not name the library add, so this is not the "+
				"failure the shim fixes:\n%s", out)
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	for _, flags := range [][]string{{"-L" + lib}, {"-L", lib}} {
		t.Run("shim: cc links -ladd through "+spelling(flags), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "main.c", addMain)
			command(t, dir, nil, "", store.role("cc"), append(append([]string{"main.c"}, flags...), "-ladd", "-o", "main")...)
			run(t, dir)
		})
	}
	// W1-F2.b: a build system that passes its arguments in an @file passes
	// its -L there. zig reads the file itself and re-roots the -L in it.
	t.Run("control: bare zig cc drops the absolute -L in an @file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "main.c", addMain)
		writeFile(t, dir, "link.rsp", "-L"+lib+" -ladd\n")
		args := append([]string{"cc", "-target", d["target"]}, shim.DarwinFlags(d["sdk"])...)
		args = append(args, "main.c", "@link.rsp", "-o", "main")
		cmd := exec.Command(store.zig, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		t.Logf("MEASURE zig %s: %v\n%s", strings.Join(args, " "), err, out)
		if err == nil {
			t.Fatal("bare zig cc found libadd.a through an absolute -L in an @file under --sysroot, so the shim's " +
				"rewriting is no longer what makes the shim's @file arms pass")
		}
	})
	for _, c := range []struct{ name, rsp string }{
		{"-L<dir>", "-L" + lib + " -ladd\n"},
		{"a comment, a quoted -L <dir> and a repeated -l", "# the library\n-L '" + lib + "'\n-ladd -ladd\n"},
	} {
		rsp := c.rsp
		t.Run("shim: cc links -ladd through an @file holding "+c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "main.c", addMain)
			writeFile(t, dir, "link.rsp", rsp)
			command(t, dir, nil, "", store.role("cc"), "main.c", "@link.rsp", "-o", "main")
			run(t, dir)
		})
	}
	for _, flags := range [][]string{{"-F" + lib}, {"-F", lib}} {
		t.Run("shim: cc compiles and links -framework Answer through "+spelling(flags), func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "main.c", "#include <stdio.h>\n#include <Answer/answer.h>\n"+
				"int main(void) { printf(\"%d\\n\", answer()); return 0; }\n")
			command(t, dir, nil, "", store.role("cc"), append(append([]string{"main.c"}, flags...), "-framework", "Answer", "-o", "main")...)
			run(t, dir)
		})
	}
}

// spelling names a -L or -F's form for a subtest: -L<dir> or "-L <dir>".
func spelling(flags []string) string {
	if len(flags) == 2 {
		return flags[0] + " <dir>"
	}
	return flags[0][:2] + "<dir>"
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
		go func(i int) {
			defer wg.Done()
			v := int(C.add(C.int(10), C.int(i)))
			mu.Lock()
			sum += v
			mu.Unlock()
		}(i)
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
