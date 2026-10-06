// Command gate is this repository's gate: the one command every change runs
// before it merges, locally and in CI.
//
//	go run ./cmd/gate
//
// It checks formatting and go.mod, vets and builds the shim with
// CGO_ENABLED=0 for every platform it ships on, and runs the tests. It is Go
// rather than a script because nothing in this repository is a shell script
// (kottera ADR-155 D3).
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// platforms are the GOOS/GOARCH pairs the shim is released for.
var platforms = [][2]string{
	{"darwin", "arm64"},
	{"darwin", "amd64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
}

type step struct {
	name string
	run  func() error
}

func main() {
	goTool, err := exec.LookPath("go")
	if err != nil {
		fail("finding the go command: %v", err)
	}
	gofmt := filepath.Join(goEnv(goTool, "GOROOT"), "bin", "gofmt")
	out, err := os.MkdirTemp("", "zig-cc-shim-gate-")
	if err != nil {
		fail("%v", err)
	}
	defer os.RemoveAll(out)

	steps := []step{
		{"gofmt -l .", func() error {
			b, err := capture(exec.Command(gofmt, "-l", "."))
			if err == nil && len(bytes.TrimSpace(b)) > 0 {
				return fmt.Errorf("not gofmt-formatted:\n%s", b)
			}
			return err
		}},
		{"go mod tidy -diff", func() error { return stream(nil, goTool, "mod", "tidy", "-diff") }},
	}
	for _, p := range platforms {
		env := []string{"CGO_ENABLED=0", "GOOS=" + p[0], "GOARCH=" + p[1]}
		label := p[0] + "/" + p[1]
		steps = append(steps,
			step{"go vet ./... (" + label + ")", func() error { return stream(env, goTool, "vet", "./...") }},
			step{"go build -trimpath ./cmd/zig-cc-shim (" + label + ", CGO_ENABLED=0)", func() error {
				return stream(env, goTool, "build", "-trimpath", "-o", filepath.Join(out, "zig-cc-shim-"+p[0]+"-"+p[1]), "./cmd/zig-cc-shim")
			}},
		)
	}
	steps = append(steps,
		step{"go build ./... (CGO_ENABLED=0)", func() error { return stream([]string{"CGO_ENABLED=0"}, goTool, "build", "./...") }},
		step{"go test -count=1 ./... (CGO_ENABLED=0)", func() error {
			return stream([]string{"CGO_ENABLED=0"}, goTool, "test", "-count=1", "./...")
		}},
	)

	start := time.Now()
	for _, s := range steps {
		fmt.Printf("gate: %s\n", s.name)
		if err := s.run(); err != nil {
			fail("FAILED at %s: %v", s.name, err)
		}
	}
	fmt.Printf("gate: ok (%d steps, %s)\n", len(steps), time.Since(start).Round(time.Second))
}

func stream(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func capture(cmd *exec.Cmd) ([]byte, error) {
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func goEnv(goTool, key string) string {
	b, err := capture(exec.Command(goTool, "env", key))
	if err != nil {
		fail("go env %s: %v", key, err)
	}
	return strings.TrimSpace(string(b))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gate: "+format+"\n", args...)
	os.Exit(1)
}
