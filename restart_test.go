package main

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lsegal/glorp/core"
)

func TestRestartHandlerShutsDownOnce(t *testing.T) {
	var requested atomic.Bool
	shutdowns, checkpoints := 0, 0
	checkpoint := func() []core.CheckpointResult {
		checkpoints++
		if shutdowns != 0 {
			t.Error("restart shut down before the checkpoint finished")
		}
		return []core.CheckpointResult{{Target: "o/r", Number: 3, Outcome: checkpointPushed}}
	}
	handler := newRestartHandler(&requested, checkpoint, func() { shutdowns++ }, func(string, ...interface{}) {}, "restart requested")
	results, err := handler(context.Background())
	if err != nil {
		t.Fatalf("first restart: %v", err)
	}
	if !requested.Load() || shutdowns != 1 || checkpoints != 1 {
		t.Fatalf("after first restart: requested = %v, shutdowns = %d, checkpoints = %d", requested.Load(), shutdowns, checkpoints)
	}
	if len(results) != 1 || results[0].Outcome != checkpointPushed {
		t.Fatalf("restart results = %+v, want the checkpoint's outcome", results)
	}
	if _, err := handler(context.Background()); err == nil {
		t.Fatal("second restart while shutting down succeeded, want an error")
	}
	if shutdowns != 1 {
		t.Fatalf("second restart shut down again: shutdowns = %d", shutdowns)
	}
}

func TestRestartExecutableUsesReplacedBinaryPath(t *testing.T) {
	if got := restartExecutable("/usr/local/bin/glorp (deleted)"); got != "/usr/local/bin/glorp" {
		t.Fatalf("restartExecutable = %q", got)
	}
	if got := restartExecutable(`C:\bin\glorp.exe`); got != `C:\bin\glorp.exe` {
		t.Fatalf("restartExecutable = %q", got)
	}
}
