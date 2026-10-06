package release

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kottera/zig-cc-shim/internal/shim"
)

// Release is one complete build: a tarball per platform and SHA256SUMS,
// held in memory until it is written.
type Release struct {
	Stamp Stamp
	// Files maps an asset name to its bytes, SHA256SUMS included.
	Files map[string][]byte
}

// Build builds the shim for every platform from the checkout at dir and
// packages it. gocache, when set, is the build cache to use instead of the
// default. A build of a modified checkout is refused unless allowModified,
// because its digests describe no commit anyone else can rebuild.
//
// Every binary is built into a temporary directory outside the checkout: a
// file left in the checkout would make the next build vcs.modified.
func Build(dir, gocache string, allowModified bool, log io.Writer) (*Release, error) {
	tmp, err := os.MkdirTemp("", "zig-cc-shim-release-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	rel := &Release{Files: map[string][]byte{}}
	for i, p := range Platforms {
		out := filepath.Join(tmp, "zig-cc-shim-"+p.OS+"-"+p.Arch)
		st, err := BuildShim(dir, out, p, gocache, log)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			rel.Stamp = st
		} else if st != rel.Stamp {
			return nil, fmt.Errorf("the %s build is stamped %+v but the %s build %+v: the checkout changed during the build", p, st, Platforms[0], rel.Stamp)
		}
		if st.Modified && !allowModified {
			return nil, fmt.Errorf("the checkout at %s has uncommitted changes (vcs.modified): a release is built from a clean commit", dir)
		}
		bin, err := os.ReadFile(out)
		if err != nil {
			return nil, err
		}
		var tgz bytes.Buffer
		if err := Tarball(&tgz, bin, shim.RoleNames(), st.Time); err != nil {
			return nil, fmt.Errorf("packaging %s: %w", p, err)
		}
		name := AssetName(st.Version, p)
		if _, err := CheckTarball(bytes.NewReader(tgz.Bytes()), shim.RoleNames()); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		rel.Files[name] = tgz.Bytes()
		fmt.Fprintf(log, "release: %s built and packaged as %s\n", p, name)
	}
	rel.Files[SumsName] = Sums(rel.Files)
	return rel, nil
}

// Sums renders SHA256SUMS over files, in sha256sum's format
// (`<hex>  <name>`), sorted by name. An entry named SumsName is skipped.
func Sums(files map[string][]byte) []byte {
	var b bytes.Buffer
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if name == SumsName {
			continue
		}
		d := sha256.Sum256(files[name])
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(d[:]), name)
	}
	return b.Bytes()
}

// Write writes every file of the release into dir, creating it.
func (r *Release) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(r.Files)) {
		if err := os.WriteFile(filepath.Join(dir, name), r.Files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Diff names every file whose bytes differ between a and b, or that only
// one of them has. Empty means the two builds are identical.
func Diff(a, b *Release) []string {
	var diffs []string
	names := slices.Sorted(maps.Keys(a.Files))
	for _, name := range names {
		other, ok := b.Files[name]
		switch {
		case !ok:
			diffs = append(diffs, name+": only in the first build")
		case !bytes.Equal(a.Files[name], other):
			diffs = append(diffs, fmt.Sprintf("%s: %s in the first build, %s in the second", name, digest(a.Files[name]), digest(other)))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(b.Files)) {
		if _, ok := a.Files[name]; !ok {
			diffs = append(diffs, name+": only in the second build")
		}
	}
	return diffs
}

func digest(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

// Load reads a release directory written by Write and verifies every file
// SHA256SUMS lists against its digest. Files SHA256SUMS does not list are
// ignored.
func Load(dir string) (*Release, error) {
	sums, err := os.ReadFile(filepath.Join(dir, SumsName))
	if err != nil {
		return nil, err
	}
	rel := &Release{Files: map[string][]byte{SumsName: sums}}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		want, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || name == "" || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("%s: malformed line %q", filepath.Join(dir, SumsName), sc.Text())
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if got := digest(b); got != want {
			return nil, fmt.Errorf("%s: sha256 %s, but %s lists %s", filepath.Join(dir, name), got, SumsName, want)
		}
		rel.Files[name] = b
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(rel.Files) != len(Platforms)+1 {
		return nil, fmt.Errorf("%s lists %d files, want one per platform (%d)", filepath.Join(dir, SumsName), len(rel.Files)-1, len(Platforms))
	}
	return rel, nil
}
