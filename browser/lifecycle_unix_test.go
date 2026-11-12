//go:build !windows

package browser

import (
	"os/exec"
	"syscall"
)

// prepareLifecycleGlorp has nothing to do on Unix: the stand-in can be
// signalled directly.
func prepareLifecycleGlorp(*exec.Cmd) {}

// signalLifecycleGlorp sends the stand-in the signal a service manager stops
// glorp with.
func signalLifecycleGlorp(cmd *exec.Cmd) error { return cmd.Process.Signal(syscall.SIGTERM) }

// processRunning reports whether pid is still a live process. A zombie counts
// as gone: it has exited and only waits for init to reap it.
func processRunning(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	var status syscall.WaitStatus
	reaped, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	return err != nil || reaped == 0
}
