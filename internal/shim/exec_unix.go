//go:build darwin || linux

package shim

import (
	"errors"
	"syscall"
)

func execve(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }

// pidAlive reports whether a process exists. Only ESRCH says it does not:
// EPERM is a process this user may not signal, which exists.
func pidAlive(pid int) bool {
	return pid > 0 && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
