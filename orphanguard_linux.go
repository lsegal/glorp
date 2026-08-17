package main

import (
	"os/exec"
	"syscall"
)

// orphanGuard names the kernel mechanism this platform uses, for the startup
// diagnostic and for tests.
const orphanGuard = "parent-death signal"

// guardOrphanedProcess asks the kernel to SIGKILL the child as soon as the
// thread that forked it dies. glorp already terminates its subprocesses on
// every exit path it can observe, but a SIGKILL of glorp itself (or a power
// loss) runs no userspace cleanup at all; the parent-death signal covers that
// gap without glorp being alive to do anything.
//
// This is only safe because owned children are forked from the pinned spawn
// thread in processSpawner, which lives for the whole life of the process.
func guardOrphanedProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// adoptOrphanedProcess has nothing to do on Linux: the guard is installed
// before the fork.
func adoptOrphanedProcess(*exec.Cmd) {}
