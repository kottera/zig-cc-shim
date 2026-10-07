package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kottera/zig-cc-shim/internal/shim"
)

var mtime = time.Date(2026, 10, 6, 22, 31, 40, 0, time.UTC)

// TestTarballRunsWhereKotteraUnpacksIt builds the real shim for this host,
// packages it, and unpacks it the way kottera's pkg/archive does for ADR-155
// D7's second fetch step (dest = "cc", strip_components = 0): through an
// os.Root, honouring only the execute bit, hard links made with Root.Link.
// With a stand-in zig at cc/zig/zig, every cc/bin/<role> runs and finds it.
func TestTarballRunsWhereKotteraUnpacksIt(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "zig-cc-shim")
	p := Platform{runtime.GOOS, runtime.GOARCH}
	if _, err := BuildShim("../..", bin, p, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	requireTrimmed(t, bin, built)
	var tgz bytes.Buffer
	if err := Tarball(&tgz, built, shim.RoleNames(), mtime); err != nil {
		t.Fatal(err)
	}
	packaged, err := CheckTarball(bytes.NewReader(tgz.Bytes()), shim.RoleNames())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packaged, built) {
		t.Fatal("the tarball's shim is not the binary that was built")
	}

	store := t.TempDir()
	if err := os.Mkdir(filepath.Join(store, "cc"), 0o755); err != nil {
		t.Fatal(err)
	}
	extractLikePkgArchive(t, filepath.Join(store, "cc"), tgz.Bytes())

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	zig := filepath.Join(store, "cc", "zig", "zig")
	build := exec.Command(goTool, "build", "-o", zig, "../../cmd/stage/testdata/fakezig")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fakezig: %v\n%s", err, out)
	}

	resolvedZig, err := filepath.EvalSymlinks(zig)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range shim.RoleNames() {
		entry := filepath.Join(store, "cc", "bin", role)
		fi, err := os.Stat(entry)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("%s has mode %v, want 0755", role, fi.Mode())
		}
		// --tera-describe runs the shim as the role it was invoked as and
		// reports the zig it found, without compiling anything.
		cmd := exec.Command(entry, shim.DescribeFlag)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), shim.EnvGlibc + "=2.31", shim.EnvMacOS + "=13.0", "SDKROOT=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s %s: %v\n%s", role, shim.DescribeFlag, err, out)
			continue
		}
		if !strings.Contains(string(out), "role: "+role+"\n") || !strings.Contains(string(out), "zig: "+resolvedZig+"\n") {
			t.Errorf("%s %s did not run as %s with %s:\n%s", role, shim.DescribeFlag, role, resolvedZig, out)
		}
	}
}

// requireTrimmed fails unless the release binary was built with -trimpath:
// its build info records the flag, and its bytes do not name the checkout it
// was built in. Without -trimpath the binary carries the checkout's absolute
// path, so builds from two checkouts differ and the release does not
// reproduce; a -check that builds twice in one checkout cannot notice that.
func requireTrimmed(t *testing.T, bin string, built []byte) {
	t.Helper()
	if !slices.Contains(BuildFlags, "-trimpath") {
		t.Errorf("BuildFlags %q lacks -trimpath", BuildFlags)
	}
	bi, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := false
	for _, s := range bi.Settings {
		trimmed = trimmed || (s.Key == "-trimpath" && s.Value == "true")
	}
	if !trimmed {
		t.Errorf("the release binary's build info does not record -trimpath=true: %v", bi.Settings)
	}
	checkout, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(built, []byte(checkout)) {
		t.Errorf("the release binary names the checkout %s", checkout)
	}
}

// extractLikePkgArchive unpacks a gzip tar into dir the way kottera's
// pkg/archive.Extract does with StripComponents 0: every write through an
// os.Root, the stored mode reduced to 0755 or 0644, a hard link made from
// the archive root.
func extractLikePkgArchive(t *testing.T, dir string, tgz []byte) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(h.Name, 0o755)
		case tar.TypeReg:
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			if err = root.MkdirAll(path.Dir(h.Name), 0o755); err != nil {
				break
			}
			var f *os.File
			if f, err = root.OpenFile(h.Name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode); err == nil {
				_, err = io.Copy(f, tr)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
		case tar.TypeLink:
			err = root.Link(h.Linkname, h.Name)
		default:
			t.Fatalf("pkg/archive would refuse %q of type %q", h.Name, string(h.Typeflag))
		}
		if err != nil {
			t.Fatalf("extracting %q: %v", h.Name, err)
		}
	}
}

func TestTarballIsDeterministic(t *testing.T) {
	roles := shim.RoleNames()
	payload := []byte("not really a shim")
	var a, b bytes.Buffer
	if err := Tarball(&a, payload, roles, mtime); err != nil {
		t.Fatal(err)
	}
	// The same instant in another zone, with sub-second noise, is the same
	// archive.
	if err := Tarball(&b, payload, roles, mtime.In(time.FixedZone("x", 3600)).Add(400*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two tarballs of the same input differ")
	}

	gz, err := gzip.NewReader(bytes.NewReader(a.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if gz.Name != "" || gz.Comment != "" || !gz.ModTime.IsZero() || gz.Extra != nil {
		t.Errorf("the gzip header carries %+v, want nothing", gz.Header)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if !h.ModTime.Equal(mtime) || len(h.PAXRecords) != 0 || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Errorf("%s: mtime %v, PAX %v, atime %v, ctime %v", h.Name, h.ModTime, h.PAXRecords, h.AccessTime, h.ChangeTime)
		}
	}
	want := []string{"bin/"}
	for _, r := range roles {
		want = append(want, "bin/"+r)
	}
	if !slices.Equal(names, want) {
		t.Errorf("entries %q, want %q", names, want)
	}
}

func TestCheckTarballRefuses(t *testing.T) {
	roles := []string{"ar", "cc"}
	reg := func(name string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o755, Size: 1}
	}
	link := func(name, target string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeLink, Name: name, Linkname: target, Mode: 0o755}
	}
	for _, tc := range []struct {
		name    string
		entries []*tar.Header
		want    string
	}{
		{"absolute", []*tar.Header{reg("/bin/ar"), link("bin/cc", "/bin/ar")}, "absolute"},
		{"dotdot", []*tar.Header{reg("bin/../ar"), link("bin/cc", "bin/ar")}, ".. component"},
		{"not executable", []*tar.Header{{Typeflag: tar.TypeReg, Name: "bin/ar", Mode: 0o644, Size: 1}, link("bin/cc", "bin/ar")}, "want 0755"},
		{"owned", []*tar.Header{{Typeflag: tar.TypeReg, Name: "bin/ar", Mode: 0o755, Size: 1, Uid: 501, Uname: "ed"}, link("bin/cc", "bin/ar")}, "owned by 501"},
		{"symlink", []*tar.Header{reg("bin/ar"), {Typeflag: tar.TypeSymlink, Name: "bin/cc", Linkname: "ar", Mode: 0o755}}, "type"},
		{"link before its target", []*tar.Header{link("bin/cc", "bin/ar"), reg("bin/ar")}, "earlier in the archive"},
		{"link outside", []*tar.Header{reg("bin/ar"), link("bin/cc", "../ar")}, "earlier in the archive"},
		{"missing role", []*tar.Header{reg("bin/ar")}, "want every role"},
		{"extra file", []*tar.Header{reg("bin/ar"), link("bin/cc", "bin/ar"), link("lib/x", "bin/ar")}, "not directly under bin/"},
		{"two copies", []*tar.Header{reg("bin/ar"), reg("bin/cc")}, "second regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			for _, h := range tc.entries {
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if h.Size > 0 {
					tw.Write([]byte("x"))
				}
			}
			tw.Close()
			gz.Close()
			_, err := CheckTarball(&buf, roles)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want a refusal mentioning %q", err, tc.want)
			}
		})
	}
}

func TestSumsWriteAndLoad(t *testing.T) {
	rel := fakeRelease("v1.2.3")
	sums := string(rel.Files[SumsName])
	lines := strings.Split(strings.TrimSpace(sums), "\n")
	if len(lines) != len(Platforms) || !strings.HasSuffix(lines[0], "  zig-cc-shim-v1.2.3-darwin-amd64.tar.gz") {
		t.Errorf("SHA256SUMS:\n%s", sums)
	}
	dir := t.TempDir()
	if err := rel.Write(dir); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := Diff(rel, loaded); len(d) != 0 {
		t.Errorf("round trip differs: %q", d)
	}
	name := AssetName("v1.2.3", Platforms[2])
	if err := os.WriteFile(filepath.Join(dir, name), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "but SHA256SUMS lists") {
		t.Errorf("a tampered asset loaded: %v", err)
	}
	other := fakeRelease("v1.2.3")
	other.Files[name] = []byte("different")
	if d := Diff(rel, other); len(d) != 1 || !strings.HasPrefix(d[0], name+": ") {
		t.Errorf("Diff = %q, want one difference in %s", d, name)
	}
}

func fakeRelease(version string) *Release {
	rel := &Release{Files: map[string][]byte{}}
	for _, p := range Platforms {
		rel.Files[AssetName(version, p)] = []byte(p.String())
	}
	rel.Files[SumsName] = Sums(rel.Files)
	return rel
}

func TestBuildEnvPinsWhatVaries(t *testing.T) {
	host := []string{"PATH=/bin", "GOFLAGS=-mod=mod", "GOAMD64=v3", "CGO_ENABLED=1", "GOOS=windows", "GOTOOLCHAIN=auto", "GOCACHE=/host"}
	env := buildEnv(host, Platform{"linux", "amd64"}, "/fresh")
	for _, want := range []string{"PATH=/bin", "GOFLAGS=", "GOAMD64=v1", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOTOOLCHAIN=local", "GOCACHE=/fresh"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s: %q", want, env)
		}
	}
	for _, gone := range []string{"GOFLAGS=-mod=mod", "GOAMD64=v3", "CGO_ENABLED=1", "GOOS=windows", "GOTOOLCHAIN=auto", "GOCACHE=/host"} {
		if slices.Contains(env, gone) {
			t.Errorf("env keeps the host's %s", gone)
		}
	}
}

func TestPublish(t *testing.T) {
	var calls []string
	uploaded := map[string][]byte{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/kottera/zig-cc-shim/releases":
			var req map[string]any
			json.Unmarshal(body, &req)
			if req["tag_name"] != "v1.2.3" || req["draft"] != true {
				http.Error(w, "bad create "+string(body), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "upload_url": srv.URL + "/upload/7/assets{?name,label}"})
		case r.Method == http.MethodPost && r.URL.Path == "/upload/7/assets":
			uploaded[r.URL.Query().Get("name")] = body
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte("{}"))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/kottera/zig-cc-shim/releases/7":
			if !strings.Contains(string(body), `"draft":false`) {
				http.Error(w, "bad patch", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "html_url": "https://example/releases/v1.2.3"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &Publisher{API: srv.URL, Repo: "kottera/zig-cc-shim", Token: "tok"}
	rel := fakeRelease("v1.2.3")

	if _, err := p.Publish(context.Background(), "v1.2.4", rel, io.Discard); err == nil || !strings.Contains(err.Error(), "another commit") {
		t.Fatalf("assets for v1.2.3 published under v1.2.4: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("a refused publish still called GitHub: %q", calls)
	}

	u, err := p.Publish(context.Background(), "v1.2.3", rel, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://example/releases/v1.2.3" {
		t.Errorf("url %q", u)
	}
	if d := Diff(rel, &Release{Files: uploaded}); len(d) != 0 {
		t.Errorf("uploaded assets differ: %q", d)
	}
	if calls[0] != "POST /repos/kottera/zig-cc-shim/releases" || calls[len(calls)-1] != "PATCH /repos/kottera/zig-cc-shim/releases/7" {
		t.Errorf("calls %q: want the draft created first and published last", calls)
	}
}
