package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TempOutputPrefix begins the name of every output the shim creates under
// $TMPDIR in place of /dev/null (DevNullOutput).
const TempOutputPrefix = "zig-cc-shim-out-"

// TempOutputGrace is how long an output is left alone after it was last
// written, whatever its process. It covers a process the shim cannot see,
// such as one in another PID namespace that shares $TMPDIR, which zig may
// still be writing.
const TempOutputGrace = 10 * time.Minute

// TempOutputs creates the outputs that replace /dev/null, and removes the
// ones whose compile is over.
//
// The shim replaces itself with zig (syscall.Exec), so nothing is left to
// remove an output once zig exits. But execve keeps the process ID, so the
// output's name records the process that writes it,
// zig-cc-shim-out-<pid>-<random><suffix>, and every later shim that creates
// one first removes those whose process has exited. The outputs under
// $TMPDIR are therefore at most one per running compile, plus those written
// in the last TempOutputGrace, plus any it cannot remove (another user's).
// Each output is still created exclusively under a random name, so two
// compiles never share one and nothing at a predictable path is written
// through.
type TempOutputs struct {
	Dir string
	// PID is this process's ID, which zig keeps.
	PID int
	Now func() time.Time
	// Alive reports whether a process exists. It is not asked about PID.
	Alive func(pid int) bool
}

// Create sweeps Dir, then creates an empty output there with suffix.
func (o TempOutputs) Create(suffix string) (string, error) {
	o.Sweep()
	f, err := os.CreateTemp(o.Dir, fmt.Sprintf("%s%d-*%s", TempOutputPrefix, o.PID, suffix))
	if err != nil {
		return "", err
	}
	return f.Name(), f.Close()
}

// Sweep removes every output in Dir that is a regular file, was last written
// more than TempOutputGrace ago, and whose process has exited. An output
// named for PID is from an earlier process with this one's ID, so it has
// exited too. One without a process ID in its name (v0.1.0 named them
// zig-cc-shim-out-<random><suffix>) is removed on age alone. Anything it
// cannot read or remove is left: a sweep never fails a compile.
func (o TempOutputs) Sweep() {
	d, err := os.Open(o.Dir)
	if err != nil {
		return
	}
	names, _ := d.Readdirnames(-1)
	d.Close()
	now := o.Now()
	for _, name := range names {
		rest, ok := strings.CutPrefix(name, TempOutputPrefix)
		if !ok {
			continue
		}
		if field, _, ok := strings.Cut(rest, "-"); ok {
			pid, err := strconv.Atoi(field)
			if err != nil || (pid != o.PID && o.Alive(pid)) {
				continue
			}
		}
		p := filepath.Join(o.Dir, name)
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < TempOutputGrace {
			continue
		}
		os.Remove(p)
	}
}
