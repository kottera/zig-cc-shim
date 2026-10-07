//go:build !(darwin || linux)

package shim

import "errors"

func execve(path string, argv, env []string) error {
	return errors.New("the shim runs on darwin and linux only")
}

func pidAlive(pid int) bool { return true }
