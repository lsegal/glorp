package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu      sync.Mutex
	calls   int
	batches [][]Issue
}

func (f *fakeSource) ListIssues(_ context.Context, _ string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.calls
	f.calls++
	if n >= len(f.batches) {
		return f.batches[len(f.batches)-1], nil
	}
	return f.batches[n], nil
}

type fakeRunner struct {
	mu          sync.Mutex
	got         []int
	active, max int
	release     chan struct{}
}

type fakeLabelEnsurer struct {
	called bool
	err    error
}

func (f *fakeLabelEnsurer) EnsureLabels(_ context.Context, _ string) error {
	f.called = true
	return f.err
}

func (f *fakeRunner) Run(ctx context.Context, i Issue) error {
	f.mu.Lock()
	f.got = append(f.got, i.Number)
	f.active++
	if f.active > f.max {
		f.max = f.active
	}
	f.mu.Unlock()
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return nil
}
func TestParseIssues(t *testing.T) {
	got, err := parseIssues([]byte(`[{"number":7,"title":"bug","state":"OPEN"}]`))
	if err != nil || len(got) != 1 || got[0].Number != 7 {
		t.Fatalf("%v %#v", err, got)
	}
}
func TestWatcherRunsUnseenIssuesWithLimit(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 1}, {Number: 2}}, {{Number: 1}, {Number: 2}, {Number: 3}, {Number: 4}}}}
	r := &fakeRunner{release: make(chan struct{})}
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{Repo: "o/r", Interval: time.Millisecond, Concurrency: 2, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: &logs}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	close(r.release)
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 4 || r.max > 2 {
		t.Fatalf("got=%v max=%d", r.got, r.max)
	}
	for _, want := range []string{"discovered 2 new issue(s)", "tasks: 2 running", "shutdown requested"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
}
func TestWatcherTreatsPreexistingUnseenIssuesAsNew(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	src := &fakeSource{batches: [][]Issue{{{Number: 1, CreatedAt: old}}}}
	r := &fakeRunner{release: make(chan struct{})}
	w := &Watcher{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	close(r.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	seen, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if !seen[1] || len(r.got) != 1 || r.got[0] != 1 {
		t.Fatalf("pre-existing unseen issue was not handled: seen=%v got=%v", seen, r.got)
	}
}
func TestInvalidRepo(t *testing.T) {
	w := &Watcher{Repo: "bad", Interval: time.Second, Concurrency: 1}
	if w.Run(context.Background()) == nil {
		t.Fatal("expected error")
	}
}

func TestWatcherEnsuresLabelsOnStart(t *testing.T) {
	labels := &fakeLabelEnsurer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &Watcher{Repo: "o/r", Interval: time.Hour, Concurrency: 1, Labels: labels, Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})}}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !labels.called {
		t.Fatal("labels were not ensured on startup")
	}
}

func TestWatcherStopsWhenLabelEnsuringFails(t *testing.T) {
	labels := &fakeLabelEnsurer{err: context.Canceled}
	w := &Watcher{Repo: "o/r", Interval: time.Hour, Concurrency: 1, Labels: labels}
	if err := w.Run(context.Background()); err != context.Canceled {
		t.Fatalf("expected label error, got %v", err)
	}
}

func TestCommandRunnerUsesSelectedAgentSyntax(t *testing.T) {
	if got := commandArgs(CommandRunner{Agent: "codex"}, Issue{Number: 12}); len(got) != 3 || got[0] != "exec" || got[1] != "--dangerously-bypass-approvals-and-sandbox" || got[2] != "/gh-fix 12" {
		t.Fatalf("codex args: %#v", got)
	}
	if got := commandArgs(CommandRunner{Agent: "claude"}, Issue{Number: 12}); len(got) != 3 || got[0] != "-p" || got[1] != "--dangerously-skip-permissions" || got[2] != "/gh-fix 12" {
		t.Fatalf("claude args: %#v", got)
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := map[int]bool{3: true, 9: true}
	if err := saveState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadState(path)
	if err != nil || len(got) != 2 || !got[3] || !got[9] {
		t.Fatalf("state error=%v value=%v", err, got)
	}
}

func TestWorkStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := map[int]workState{7: {Status: "active", SessionID: "session-7"}}
	if err := saveWorkState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadWorkState(path)
	if err != nil || got[7] != want[7] {
		t.Fatalf("state error=%v value=%v", err, got)
	}
}

func TestWatcherPersistsSessionIDAfterCompletion(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}}}
	r := &fakeRunner{release: make(chan struct{})}
	w := &Watcher{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		StatePath: statePath, Issues: src, Runner: r,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	var active workState
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := loadWorkState(statePath)
		if err == nil && state[7].Status == "active" {
			active = state[7]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if active.SessionID == "" {
		cancel()
		<-done
		t.Fatal("active session ID was not persisted")
	}
	close(r.release)

	var completed workState
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := loadWorkState(statePath)
		if err == nil && state[7].Status == "completed" {
			completed = state[7]
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if completed.SessionID != active.SessionID {
		t.Fatalf("completed state session ID = %q, want %q", completed.SessionID, active.SessionID)
	}
}

func TestHasAgentStartedLabel(t *testing.T) {
	issue := Issue{Labels: []IssueLabel{{Name: "agent-ready"}, {Name: agentStartedLabel}}}
	if !hasLabel(issue, agentStartedLabel) {
		t.Fatal("agent-started label was not found")
	}
}
