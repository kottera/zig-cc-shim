// Package release builds the shim's release: one gzip tar per platform whose
// entries are bin/<role>, plus a SHA256SUMS over the tarballs (kottera
// ADR-155 D3 and D7). Two builds of one commit produce identical bytes, so
// anyone can rebuild a release and check its digests.
package release

import (
	"debug/buildinfo"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ShimPackage is the package every role entry is a build of.
const ShimPackage = "github.com/kottera/zig-cc-shim/cmd/zig-cc-shim"

// Platform is one GOOS/GOARCH pair the shim is released for.
type Platform struct{ OS, Arch string }

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// Platforms are the platforms a release carries, in the order they are
// built and listed.
var Platforms = []Platform{
	{"darwin", "arm64"},
	{"darwin", "amd64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
}

// BuildFlags are the go build flags of a release build. -trimpath drops the
// checkout's path; -buildvcs=true stamps the commit (and makes a build
// outside a git checkout fail rather than silently go unstamped); an empty
// -buildid leaves nothing in the binary that names the build itself.
var BuildFlags = []string{"-trimpath", "-buildvcs=true", "-ldflags=-buildid="}

// fixedEnv is every environment variable the build pins. Anything a host
// could set that changes the output is either here or cleared by buildEnv.
func fixedEnv(p Platform) []string {
	return []string{
		"CGO_ENABLED=0",
		"GOOS=" + p.OS,
		"GOARCH=" + p.Arch,
		// The default instruction-set levels, stated so that a host's
		// setting cannot raise them.
		"GOAMD64=v1",
		"GOARM64=v8.0",
		"GOFLAGS=",
		"GOEXPERIMENT=",
		// The go command on PATH builds the release; it never downloads
		// another toolchain on go.mod's say-so.
		"GOTOOLCHAIN=local",
	}
}

// buildEnv is base with every variable fixedEnv names replaced, plus
// GOCACHE when gocache is set.
func buildEnv(base []string, p Platform, gocache string) []string {
	fixed := fixedEnv(p)
	if gocache != "" {
		fixed = append(fixed, "GOCACHE="+gocache)
	}
	drop := map[string]bool{}
	for _, kv := range fixed {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	var env []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	return append(env, fixed...)
}

// Stamp is what a release build recorded about the commit it was built
// from.
type Stamp struct {
	// Version is the module version Go stamped: the tag when the commit
	// is tagged, otherwise a pseudo-version.
	Version  string
	Revision string
	// Time is the commit time, which every tar entry carries.
	Time     time.Time
	Modified bool
}

// safeVersion is what a version must look like to become part of an asset
// name: a tag or a pseudo-version, never "(devel)".
var safeVersion = regexp.MustCompile(`^v[0-9A-Za-z.+-]+$`)

// BuildShim builds the shim for p into out with BuildFlags, running go in
// dir (the repository checkout). gocache, when set, is the build cache to
// use instead of the default.
func BuildShim(dir, out string, p Platform, gocache string, log io.Writer) (Stamp, error) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		return Stamp{}, fmt.Errorf("finding the go command: %w", err)
	}
	args := append([]string{"build"}, BuildFlags...)
	args = append(args, "-o", out, ShimPackage)
	cmd := exec.Command(goTool, args...)
	cmd.Dir = dir
	cmd.Env = buildEnv(os.Environ(), p, gocache)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return Stamp{}, fmt.Errorf("building %s for %s: %w", ShimPackage, p, err)
	}
	return ReadStamp(out)
}

// ReadStamp reads the version and VCS stamp from a built shim.
func ReadStamp(binary string) (Stamp, error) {
	bi, err := buildinfo.ReadFile(binary)
	if err != nil {
		return Stamp{}, fmt.Errorf("reading the build info of %s: %w", binary, err)
	}
	s := Stamp{Version: bi.Main.Version}
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			s.Revision = kv.Value
		case "vcs.modified":
			s.Modified = kv.Value == "true"
		case "vcs.time":
			if s.Time, err = time.Parse(time.RFC3339, kv.Value); err != nil {
				return Stamp{}, fmt.Errorf("%s: vcs.time %q: %w", binary, kv.Value, err)
			}
		}
	}
	switch {
	case s.Revision == "" || s.Time.IsZero():
		return Stamp{}, fmt.Errorf("%s carries no VCS stamp: a release is built from a git checkout", binary)
	case !safeVersion.MatchString(s.Version):
		return Stamp{}, fmt.Errorf("%s has module version %q, which is neither a tag nor a pseudo-version", binary, s.Version)
	}
	return s, nil
}

// AssetName is the release asset for one platform. It is keyed by
// <os>-<arch> so that a toolchain definition can key its url and sha256
// per platform.
func AssetName(version string, p Platform) string {
	return fmt.Sprintf("zig-cc-shim-%s-%s-%s.tar.gz", version, p.OS, p.Arch)
}

// SumsName is the release's digest file.
const SumsName = "SHA256SUMS"
