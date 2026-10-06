//go:build darwin || linux

package shim

import "syscall"

func execve(path string, argv, env []string) error { return syscall.Exec(path, argv, env) }
