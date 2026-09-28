//go:build !windows

package main

import "syscall"

// relaunchProcess replaces this process with a new glorp, keeping its process
// id and terminal. It only returns when the exec fails.
func relaunchProcess(executable string, args, env []string) (int, error) {
	return 1, syscall.Exec(executable, args, env)
}
