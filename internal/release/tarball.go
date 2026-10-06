package release

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
)

// Tarball writes one platform's gzip tar to w. Its entries are, in this
// order: the directory bin/, the shim as the regular file bin/<roles[0]>,
// and every other role as a hard link to it (the same layout cmd/stage
// writes, and for the same reason: a role keeps its own name however a build
// tool resolves it).
//
// Nothing in it varies between builds: every entry has mode 0755, owner and
// group 0 with no names, and mtime as its only time; the format is USTAR, so
// there are no PAX records to carry access or change times; and the gzip
// header carries no name and no time. With the same shim bytes and the same
// mtime, the output is the same bytes.
func Tarball(w io.Writer, shimBinary []byte, roles []string, mtime time.Time) error {
	if len(roles) == 0 {
		return errors.New("no roles to package")
	}
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	mtime = mtime.UTC().Truncate(time.Second)
	hdr := func(name string, typ byte) *tar.Header {
		return &tar.Header{Typeflag: typ, Name: name, Mode: 0o755, ModTime: mtime, Format: tar.FormatUSTAR}
	}
	if err := tw.WriteHeader(hdr("bin/", tar.TypeDir)); err != nil {
		return err
	}
	first := "bin/" + roles[0]
	h := hdr(first, tar.TypeReg)
	h.Size = int64(len(shimBinary))
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	if _, err := tw.Write(shimBinary); err != nil {
		return err
	}
	for _, role := range roles[1:] {
		h := hdr("bin/"+role, tar.TypeLink)
		h.Linkname = first
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// CheckTarball reads a release tarball and refuses it unless it is shaped
// for kottera's pkg/archive, extracted with dest = "cc" and
// strip_components = 0 (ADR-155 D7):
//
//   - gzip, then tar, and nothing in it but the bin/ directory, regular
//     files and hard links;
//   - no absolute name and no `..`; every name is clean and under bin/;
//   - every regular file and link has mode 0755, owner and group 0;
//   - every hard link names a regular file that comes before it, so the
//     extractor's link finds its target and stays inside the root;
//   - exactly one entry per role, and nothing else.
//
// It returns the shim's bytes, so a caller can check what was packaged.
func CheckTarball(r io.Reader, roles []string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not gzip: %w", err)
	}
	tr := tar.NewReader(gz)
	var (
		problems []string
		files    = map[string]bool{}
		seen     []string
		shim     []byte
	)
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the tar: %w", err)
		}
		name := h.Name
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		switch {
		case path.IsAbs(h.Name) || strings.Contains(h.Name, `\`):
			bad("%q is absolute or not slash-separated", h.Name)
			continue
		case slices.Contains(strings.Split(name, "/"), ".."):
			bad("%q has a .. component", h.Name)
			continue
		case path.Clean(name) != name:
			bad("%q is not a clean path", h.Name)
			continue
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			bad("%q is owned by %d:%d (%q:%q), want 0:0 with no names", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if h.Mode != 0o755 {
			bad("%q has mode %#o, want 0755", h.Name, h.Mode)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if name != "bin" {
				bad("directory %q: the only directory is bin/", h.Name)
			}
			continue
		case tar.TypeReg:
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("reading %q: %w", h.Name, err)
			}
			if shim != nil {
				bad("%q is a second regular file: every role but the first is a hard link", h.Name)
			}
			shim = b
			files[name] = true
		case tar.TypeLink:
			if !files[h.Linkname] {
				bad("hard link %q -> %q does not name a regular file earlier in the archive", h.Name, h.Linkname)
			}
		default:
			bad("%q is type %q: only bin/, regular files and hard links belong in a release", h.Name, string(h.Typeflag))
			continue
		}
		if dir, base := path.Split(name); dir != "bin/" {
			bad("%q is not directly under bin/", h.Name)
		} else {
			seen = append(seen, base)
		}
	}
	slices.Sort(seen)
	want := slices.Clone(roles)
	slices.Sort(want)
	if !slices.Equal(seen, want) {
		bad("bin/ holds %q, want every role %q", seen, want)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the tarball is not shaped for pkg/archive:\n  %s", strings.Join(problems, "\n  "))
	}
	return shim, nil
}
