package shim

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestTempOutputsBounded is W1-F2.a: compile after compile to /dev/null, each
// in a process that then exits, leaves one output under $TMPDIR, not one per
// compile.
func TestTempOutputsBounded(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	for i := range 50 {
		o := TempOutputs{
			Dir:   dir,
			PID:   1000 + i,
			Now:   func() time.Time { return start.Add(time.Duration(i) * (TempOutputGrace + time.Second)) },
			Alive: func(int) bool { return false },
		}
		path, err := o.Create(".o")
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), TempOutputPrefix+strconv.Itoa(o.PID)+"-") || !strings.HasSuffix(path, ".o") {
			t.Fatalf("output %s is not %s<pid>-*.o in %s", path, TempOutputPrefix, dir)
		}
		// The compile writes it with the clock the sweep reads.
		if err := os.Chtimes(path, o.Now(), o.Now()); err != nil {
			t.Fatal(err)
		}
		if got := listDir(t, dir); len(got) != 1 || got[0] != filepath.Base(path) {
			t.Fatalf("after compile %d: %q, want only %s", i, got, filepath.Base(path))
		}
	}
}

func TestTempOutputsSweep(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-TempOutputGrace - time.Minute)
	recent := now.Add(-TempOutputGrace / 2)
	const self, live = 500, 501
	files := map[string]time.Time{
		TempOutputPrefix + "502-1.o":  old,    // removed: its process exited
		TempOutputPrefix + "500-2.o":  old,    // removed: an earlier process with this one's ID
		TempOutputPrefix + "123456.o": old,    // removed: v0.1.0's name, on age alone
		TempOutputPrefix + "501-3.o":  old,    // kept: its process is running
		TempOutputPrefix + "502-4.o":  recent, // kept: inside the grace period
		TempOutputPrefix + "654321":   recent, // kept: v0.1.0's name, inside the grace period
		TempOutputPrefix + "x-5.o":    old,    // kept: not a process ID
		"zig-cc-shim-other-502-6.o":   old,    // kept: not an output
		"unrelated":                   old,    // kept
		TempOutputPrefix + "502-dir":  old,    // kept: a directory, made below
	}
	for name, mtime := range files {
		p := filepath.Join(dir, name)
		var err error
		switch {
		case strings.HasSuffix(name, "-dir"):
			err = os.Mkdir(p, 0o755)
		default:
			err = os.WriteFile(p, nil, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	var asked []int
	o := TempOutputs{
		Dir: dir, PID: self,
		Now: func() time.Time { return now },
		Alive: func(pid int) bool {
			asked = append(asked, pid)
			return pid == live
		},
	}
	o.Sweep()
	want := []string{
		"unrelated", "zig-cc-shim-other-502-6.o",
		TempOutputPrefix + "501-3.o", TempOutputPrefix + "502-4.o", TempOutputPrefix + "502-dir",
		TempOutputPrefix + "654321", TempOutputPrefix + "x-5.o",
	}
	if got := listDir(t, dir); !slices.Equal(got, want) {
		t.Errorf("after the sweep\n got %q\nwant %q", got, want)
	}
	if slices.Contains(asked, self) {
		t.Errorf("the sweep asked whether its own process is alive")
	}
	// A directory it cannot read is not an error: the compile goes on.
	o.Dir = filepath.Join(dir, "missing")
	o.Sweep()
}

func TestTempOutputsCreateIsUnique(t *testing.T) {
	dir := t.TempDir()
	o := TempOutputs{Dir: dir, PID: 7, Now: time.Now, Alive: func(int) bool { return true }}
	a, err := o.Create(".o")
	if err != nil {
		t.Fatal(err)
	}
	b, err := o.Create(".o")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two outputs share %s", a)
	}
	o.Dir = filepath.Join(dir, "missing")
	if _, err := o.Create(".o"); err == nil {
		t.Errorf("an output in a missing directory was created")
	}
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Errorf("this process is not alive")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Errorf("a pid that is no process is alive")
	}
}
