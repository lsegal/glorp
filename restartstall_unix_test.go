//go:build !windows

package main

import (
	"syscall"
	"testing"
	"time"

	"github.com/lsegal/glorp/process"
)

// SIGTERM takes the same shutdown checkpoint as the restart button (issue
// #741) and must likewise end with every agent stopped (issue #749).
func TestSignalShutdownStopsPausedAgents(t *testing.T) {
	_, checkout := checkpointRepo(t)
	writeCheckpointFile(t, checkout+"/work.txt", "unsaved\n")
	agents := &activeAgents{}
	s := newShutdownCheckpointer(agents, func(string, ...interface{}) {})
	ctx, stop := process.ShutdownContext(func() { s.Checkpoint() })
	defer stop()
	exited := startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: 7}, checkout)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Minute):
		t.Fatal("SIGTERM never finished the checkpoint and began shutting down")
	}
	if results := s.Checkpoint(); len(results) != 1 || results[0].Outcome != checkpointPushed {
		t.Fatalf("checkpoint = %+v, want the run's work pushed", results)
	}
	awaitExit(t, 20*time.Second, exited)
}
