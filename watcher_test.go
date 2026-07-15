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
func TestWatcherSeedsThenRunsNewIssuesWithLimit(t *testing.T) {
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
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 2 || r.max > 2 {
		t.Fatalf("got=%v max=%d", r.got, r.max)
	}
	for _, want := range []string{"established the baseline", "discovered 2 new issue(s)", "tasks: 2 running", "shutdown requested"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestWatcherDoesNotMarkIssuesCreatedBeforeStartupSeen(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	src := &fakeSource{batches: [][]Issue{{{Number: 1, CreatedAt: old}}}}
	w := &Watcher{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state"), Issues: src, Runner: &fakeRunner{release: make(chan struct{})},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	seen, err := loadState(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if seen[1] {
		t.Fatalf("pre-existing issue was marked seen: %v", seen)
	}
}
func TestInvalidRepo(t *testing.T) {
	w := &Watcher{Repo: "bad", Interval: time.Second, Concurrency: 1}
	if w.Run(context.Background()) == nil {
		t.Fatal("expected error")
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
