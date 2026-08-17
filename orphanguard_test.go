//go:build !windows

package main

import (
	"os/exec"
	"runtime"
	"sync"
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
