package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
	"github.com/lsegal/glorp/process"
)

const stallHelperEnv = "GLORP_STALL_HELPER"

// TestRestartStallHelperProcess is not a real test: the restart tests run the
// test binary as a stand-in agent. As "agent" it starts a child that shares
// its output, as a real agent's shell tools and subagents do, then waits
// forever; as "child" it only waits forever.
func TestRestartStallHelperProcess(t *testing.T) {
	switch os.Getenv(stallHelperEnv) {
	case "agent":
		child := exec.Command(os.Args[0], "-test.run=^TestRestartStallHelperProcess$")
		child.Env = append(os.Environ(), stallHelperEnv+"=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("agent ready\n")
		time.Sleep(time.Hour)
	case "child":
		time.Sleep(time.Hour)
	}
}

// lockedBuffer is an agent output sink that is safe to read while the agent
// is writing to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startStallAgent starts a stand-in agent the way CommandRunner does, tracked
// for the shutdown checkpoint, and reports on the returned channel once
// waiting for it has returned.
func startStallAgent(t *testing.T, ctx context.Context, agents *activeAgents, issue Issue, checkout string) <-chan error {
	t.Helper()
	cmd := newAgentCommand(ctx, os.Args[0], "-test.run=^TestRestartStallHelperProcess$")
	cmd.Env = append(os.Environ(), stallHelperEnv+"=agent")
	output := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = output, output
	run := agents.add(issue, checkout)
	if err := process.Start(cmd); err != nil {
		t.Fatalf("start stand-in agent: %v", err)
	}
	agents.started(run, cmd)
	t.Cleanup(func() { _ = process.Kill(cmd) })
	exited := make(chan error, 1)
	go func() {
		err := process.Wait(cmd)
		agents.remove(run)
		exited <- err
	}()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(output.String(), "agent ready") {
		if time.Now().After(deadline) {
			t.Fatalf("stand-in agent never started its child: %q", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return exited
}

// awaitExit fails the test unless every agent has exited within budget, the
// way shutdown must before glorp can relaunch.
func awaitExit(t *testing.T, budget time.Duration, exits ...<-chan error) {
	t.Helper()
	deadline := time.After(budget)
	for i, exited := range exits {
		select {
		case <-exited:
		case <-deadline:
			t.Fatalf("agent %d was still running %s after shutdown; the restart would never relaunch", i+1, budget)
		}
	}
}

// The stall in issue #749: the checkpoint pauses an agent's whole process
// tree, and stopping the run then killed only the agent, so its paused child
// kept the output pipe open and waiting for the agent never returned.
func TestCancelledAgentExitsWhenItsTreeIsPaused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := &activeAgents{}
	exited := startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: 1}, "")
	agents.pauseAll()
	cancel()
	awaitExit(t, 20*time.Second, exited)
}

// A clean run checkpoints as a no-op, and the restart still goes on to stop
// its agent.
func TestRestartWithCleanRunStopsAgent(t *testing.T) {
	_, checkout := checkpointRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := &activeAgents{}
	exited := startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: 7}, checkout)
	results := restartForTest(t, agents, cancel, nil, time.Minute, time.Minute)
	if len(results) != 1 || results[0].Outcome != checkpointNothing {
		t.Fatalf("checkpoint = %+v, want nothing to push", results)
	}
	awaitExit(t, 20*time.Second, exited)
}

// Several runs, clean and dirty, are checkpointed in parallel, the dirty ones'
// work reaches their remotes, and every agent is stopped afterwards.
func TestRestartWithDirtyRunsPushesThenStopsAgents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := &activeAgents{}
	var exits []<-chan error
	var remotes []string
	for number := 1; number <= 3; number++ {
		remote, checkout := checkpointRepo(t)
		if number != 2 {
			writeCheckpointFile(t, filepath.Join(checkout, "work.txt"), "unsaved\n")
		}
		remotes = append(remotes, remote)
		exits = append(exits, startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: number}, checkout))
	}
	results := restartForTest(t, agents, cancel, nil, time.Minute, time.Minute)
	want := []string{checkpointPushed, checkpointNothing, checkpointPushed}
	for i, result := range results {
		if result.Outcome != want[i] {
			t.Fatalf("run #%d checkpoint = %+v, want %s", i+1, result, want[i])
		}
	}
	for i, remote := range remotes {
		if i == 1 {
			continue
		}
		if subject := gitIn(t, remote, "log", "-1", "--format=%s", "fix/issue-7-x"); !strings.HasPrefix(subject, "Shutdown checkpoint") {
			t.Fatalf("remote %d head = %q, want the shutdown checkpoint", i+1, subject)
		}
	}
	awaitExit(t, 20*time.Second, exits...)
}

// A push that hangs, here on a pre-push hook that never finishes, is cut off
// at the run budget even though the hook's own child holds git's output open.
// The run is reported as timed out, the other run's pushed work stays, and
// both agents are stopped.
//
// The budget is shared by both runs, so it has to cover the healthy run's
// whole checkpoint, about fifteen git processes, on a slow runner too. A 3s
// budget timed it out on Windows CI (issue #761).
func TestRestartWithHangingPushTimesOutAndStopsAgents(t *testing.T) {
	const budget = 15 * time.Second
	_, hung := checkpointRepo(t)
	writeCheckpointFile(t, filepath.Join(hung, "work.txt"), "unsaved\n")
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte("#!/bin/sh\nsleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, hung, "config", "core.hooksPath", hooks)
	okRemote, ok := checkpointRepo(t)
	writeCheckpointFile(t, filepath.Join(ok, "work.txt"), "unsaved\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := &activeAgents{}
	exits := []<-chan error{
		startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: 1}, hung),
		startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: 2}, ok),
	}
	start := time.Now()
	results := restartForTest(t, agents, cancel, nil, budget, time.Minute)
	if elapsed := time.Since(start); elapsed > budget+15*time.Second {
		t.Fatalf("checkpoint took %s, want the hung push cut off at its %s run budget", elapsed, budget)
	}
	if results[0].Outcome != checkpointTimedOut {
		t.Fatalf("hung push = %+v, want timed out", results[0])
	}
	if results[1].Outcome != checkpointPushed {
		t.Fatalf("other run = %+v, want pushed", results[1])
	}
	if subject := gitIn(t, okRemote, "log", "-1", "--format=%s", "fix/issue-7-x"); !strings.HasPrefix(subject, "Shutdown checkpoint") {
		t.Fatalf("other run's remote head = %q, want its checkpoint kept", subject)
	}
	awaitExit(t, 20*time.Second, exits...)
}

// A run whose git ignores its deadline altogether is reported as timed out at
// the phase budget, and the restart still stops its agent.
func TestRestartWithHungCheckpointStopsAgentWithinBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := &activeAgents{}
	exited := startStallAgent(t, ctx, agents, Issue{Target: "o/r", Number: 5}, t.TempDir())
	block := make(chan struct{})
	defer close(block)
	hang := func(context.Context, string, ...string) (string, error) {
		<-block
		return "", errors.New("unreachable")
	}
	results := restartForTest(t, agents, cancel, hang, time.Hour, 500*time.Millisecond)
	if len(results) != 1 || results[0].Outcome != checkpointTimedOut {
		t.Fatalf("checkpoint = %+v, want timed out", results)
	}
	awaitExit(t, 20*time.Second, exited)
}

// restartForTest presses the restart button against agents, with git (when
// given) standing in for the checkpoint's git and the given budgets, and
// checks that the progress shows every run pending before it finishes.
func restartForTest(t *testing.T, agents *activeAgents, shutdown func(), git checkpointGit, perRun, overall time.Duration) []core.CheckpointResult {
	t.Helper()
	s := newShutdownCheckpointer(agents, func(string, ...interface{}) {})
	s.run, s.overall = perRun, overall
	if git != nil {
		s.git = git
	}
	var requested atomic.Bool
	handler := newRestartHandler(&requested, s.Checkpoint, shutdown, func(string, ...interface{}) {}, "restart requested")
	results, err := handler(context.Background())
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !requested.Load() {
		t.Fatal("restart was not recorded, so glorp would not relaunch")
	}
	if progress := s.Progress(); len(progress) != len(results) {
		t.Fatalf("progress = %+v, want one entry per run", progress)
	}
	for i, result := range s.Progress() {
		if result != results[i] {
			t.Fatalf("progress after the checkpoint = %+v, want the results %+v", s.Progress(), results)
		}
	}
	return results
}

// While the checkpoint runs, each run is reported pending until its outcome
// lands, and a run cut off by the budget is reported as timed out.
func TestShutdownCheckpointProgressShowsPendingRuns(t *testing.T) {
	agents := &activeAgents{}
	agents.add(Issue{Target: "o/r", Number: 1}, t.TempDir())
	agents.add(Issue{Target: "o/r", Number: 2}, "")
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	s := newShutdownCheckpointer(agents, func(string, ...interface{}) {})
	s.overall = 2 * time.Second
	s.git = func(ctx context.Context, dir string, args ...string) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return "", errors.New("released")
	}
	defer close(release)
	if progress := s.Progress(); len(progress) != 0 {
		t.Fatalf("progress before the checkpoint = %+v, want none", progress)
	}
	done := make(chan []core.CheckpointResult, 1)
	go func() { done <- s.Checkpoint() }()
	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		progress := s.Progress()
		if len(progress) == 2 && progress[0].Outcome == checkpointPending && progress[1].Outcome == checkpointNothing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("progress = %+v, want #1 pending and #2 done", progress)
		}
		time.Sleep(10 * time.Millisecond)
	}
	results := <-done
	if results[0].Outcome != checkpointTimedOut {
		t.Fatalf("results = %+v, want #1 timed out", results)
	}
	if progress := s.Progress(); progress[0].Outcome != checkpointTimedOut {
		t.Fatalf("progress after the budget = %+v, want #1 timed out", progress)
	}
}

// Once shutdown starts, glorp waits a bounded time for the watch to wind down
// and then goes ahead, so one stuck run cannot hold a restart forever.
func TestRunUntilStoppedBoundsShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stuck := make(chan struct{})
	defer close(stuck)
	var logged []string
	result := make(chan error, 1)
	go func() {
		result <- runUntilStopped(ctx, func(context.Context) error {
			<-stuck
			return nil
		}, 100*time.Millisecond, func(format string, args ...interface{}) { logged = append(logged, format) })
	}()
	select {
	case <-result:
		t.Fatal("watch returned before shutdown started")
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runUntilStopped = %v, want nil so the restart relaunches", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waited forever on a stuck watch")
	}
	if len(logged) != 1 {
		t.Fatalf("logged %q, want the stuck shutdown reported", logged)
	}
}

func TestRunUntilStoppedReturnsWatchResult(t *testing.T) {
	want := errors.New("boom")
	if err := runUntilStopped(context.Background(), func(context.Context) error { return want }, time.Hour, func(string, ...interface{}) {}); err != want {
		t.Fatalf("runUntilStopped = %v, want %v", err, want)
	}
}
