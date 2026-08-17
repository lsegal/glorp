package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGuardOrphanedProcessSetsParentDeathSignal(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	guardOrphanedProcess(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("owned child was not given a parent-death signal")
	}
}

// The acceptance case from issue #264: SIGKILL glorp and its subprocesses must
// go with it, even though no userspace cleanup can run.
func TestKilledParentTakesItsChildrenDown(t *testing.T) {
	parent := exec.Command(os.Args[0], "-test.run=TestOrphanGuardHelperProcess", "-test.timeout=60s")
	parent.Env = append(os.Environ(), "GLORP_ORPHAN_HELPER=1")
	stdout, err := parent.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe helper stdout: %v", err)
	}
	if err := parent.Start(); err != nil {
		t.Fatalf("start helper parent: %v", err)
	}
	defer func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
	}()

	var pid int
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if child, err := strconv.Atoi(strings.TrimPrefix(line, "child-pid ")); err == nil && strings.HasPrefix(line, "child-pid ") {
			pid = child
			break
		}
	}
	if pid <= 0 {
		t.Fatalf("helper parent never reported a child pid")
	}
	if !processAlive(pid) {
		t.Fatalf("helper child %d never started", pid)
	}

	if err := parent.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper parent: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return !processAlive(pid) }) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d outlived a SIGKILLed parent", pid)
	}
}

// TestOrphanGuardHelperProcess is not a test: it is the child half of
// TestKilledParentTakesItsChildrenDown, re-executed as its own process so it
// can be killed outright.
func TestOrphanGuardHelperProcess(t *testing.T) {
	if os.Getenv("GLORP_ORPHAN_HELPER") != "1" {
		t.Skip("helper process for TestKilledParentTakesItsChildrenDown")
	}
	cmd := exec.Command("sleep", "60")
	if err := startChildProcess(cmd); err != nil {
		t.Fatalf("start child process: %v", err)
	}
	fmt.Printf("child-pid %d\n", cmd.Process.Pid)
	os.Stdout.Sync()
	time.Sleep(50 * time.Second)
}
