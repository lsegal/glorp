//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Linux delivers the parent-death signal when the *thread* that forked the
// child exits, and Go retires idle threads freely, so a child forked from an
// ordinary goroutine would die at an unpredictable moment while glorp is still
// running. Churn threads hard and prove the child is untouched.
func TestOwnedChildSurvivesThreadChurn(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := startChildProcess(cmd); err != nil {
		t.Fatalf("start child process: %v", err)
	}
	t.Cleanup(func() { _ = stopChildProcess(cmd) })
	pid := cmd.Process.Pid

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Returning while still locked destroys the OS thread, which is
			// exactly what must not take a child down with it.
			runtime.LockOSThread()
			time.Sleep(5 * time.Millisecond)
		}()
	}
	wg.Wait()
	runtime.GC()
	runtime.GC()
	time.Sleep(500 * time.Millisecond)

	if !processAlive(pid) {
		t.Fatalf("child %d was killed while glorp was still running", pid)
	}
}

// Every owned child must be forked from the one pinned spawn thread, including
// children started concurrently from many goroutines.
func TestSpawnerStartsChildrenFromEveryGoroutine(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command("sh", "-c", "exit 0")
			if err := startChildProcess(cmd); err != nil {
				t.Errorf("start child process: %v", err)
				return
			}
			if err := waitChildProcess(cmd); err != nil {
				t.Errorf("wait child process: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestSpawnerReportsStartFailures(t *testing.T) {
	cmd := exec.Command("glorp-no-such-binary-exists")
	if err := startChildProcess(cmd); err == nil {
		t.Fatalf("starting a missing binary succeeded")
	}
	if isTracked(cmd) {
		t.Fatalf("failed start is tracked")
	}
}

// The acceptance case from issues #264 and #266: SIGKILL glorp and its
// subprocesses must go with it on every Unix, even though no userspace cleanup
// can run.
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
