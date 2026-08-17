//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// isolateProcessTree gives the child its own console process group so console
// signals aimed at glorp are not forwarded to it before glorp can shut it down
// in order.
func isolateProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// signalProcessTree ends the child and everything it spawned. Windows has no
// process-group signalling, so taskkill's /T flag walks the tree instead. It
// reports os.ErrProcessDone once the tree is gone.
func signalProcessTree(cmd *exec.Cmd, signal processSignal) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return os.ErrProcessDone
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if signal == checkSignal {
		if cmd.ProcessState != nil {
			return os.ErrProcessDone
		}
		return nil
	}
	args := []string{"/T", "/PID", pid}
	if signal == killSignal {
		args = append([]string{"/F"}, args...)
	}
	if err := exec.Command("taskkill", args...).Run(); err != nil {
		// taskkill without /F cannot end a process that has no window, so fall
		// back to ending the child directly rather than leaving it running.
		if killErr := cmd.Process.Kill(); killErr != nil {
			return os.ErrProcessDone
		}
	}
	return nil
}
