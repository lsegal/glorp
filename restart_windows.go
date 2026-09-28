//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
)

// relaunchProcess runs a new glorp on this console and waits for it, since
// Windows cannot replace a running process's image. Waiting keeps the shell
// that started glorp from reclaiming the console while the new instance still
// writes to it, and Ctrl+C is left to the new instance, which receives it too.
func relaunchProcess(executable string, args, env []string) (int, error) {
	signal.Ignore(os.Interrupt)
	cmd := exec.Command(executable, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
