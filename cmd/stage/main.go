// Command stage builds the shim from this repository and lays out a
// store-shaped directory around it (kottera ADR-155 D7):
//
//	<dest>/bin/<role>   one entry per role, all hard links to one build
//	<dest>/zig/zig      not written here: an unpacked zig release goes here
//
// so that <dest>/bin/cc finds <dest>/zig/zig. Run it from the repository:
//
//	go run ./cmd/stage -dest <dir> [-goos linux -goarch amd64]
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/kottera/zig-cc-shim/internal/shim"
)

const shimPackage = "github.com/kottera/zig-cc-shim/cmd/zig-cc-shim"

func main() {
	dest := flag.String("dest", "", "the directory to stage into; it becomes the store's cc/ (required)")
	goos := flag.String("goos", runtime.GOOS, "the GOOS to build the shim for")
	goarch := flag.String("goarch", runtime.GOARCH, "the GOARCH to build the shim for")
	flag.Parse()
	if *dest == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := Stage(*dest, *goos, *goarch, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "stage:", err)
		os.Exit(1)
	}
}

// Stage builds the shim with CGO_ENABLED=0 and -trimpath and writes
// <dest>/bin/<role> for every role as hard links to that one build. Existing
// role entries are replaced; nothing else in dest is touched.
func Stage(dest, goos, goarch string, out io.Writer) error {
	bin := filepath.Join(dest, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("finding the go command: %w", err)
	}
	built := filepath.Join(bin, ".zig-cc-shim.build")
	cmd := exec.Command(goTool, "build", "-trimpath", "-o", built, shimPackage)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	cmd.Stdout, cmd.Stderr = out, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s for %s/%s: %w", shimPackage, goos, goarch, err)
	}
	defer os.Remove(built)
	for _, role := range shim.RoleNames() {
		entry := filepath.Join(bin, role)
		if err := os.Remove(entry); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Link(built, entry); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "staged %d roles in %s for %s/%s\n", len(shim.RoleNames()), bin, goos, goarch)
	zig := filepath.Join(dest, "zig", "zig")
	if _, err := os.Stat(zig); err != nil {
		fmt.Fprintf(out, "note: %s does not exist yet; unpack a zig release into %s (see the README)\n", zig, filepath.Join(dest, "zig"))
	}
	return nil
}
