package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-isatty"
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

type fakeOutputRunner struct {
	reported AgentSession
}

func (fakeOutputRunner) AgentName() string                { return "codex" }
func (fakeOutputRunner) Run(context.Context, Issue) error { return nil }

func (f fakeOutputRunner) RunWithOutput(_ context.Context, _ Issue, output io.Writer) error {
	_, err := io.WriteString(output, "agent output\n")
	return err
}

func (f fakeOutputRunner) RunSessionWithOutput(_ context.Context, _ Issue, _ AgentSession, update func(AgentSession), output io.Writer) error {
	if f.reported.ID != "" || f.reported.CheckoutDirectory != "" {
		update(f.reported)
	}
	return f.RunWithOutput(context.Background(), Issue{}, output)
}

type fakeSessionRunner struct {
	agent    string
	sessions chan AgentSession
	reported AgentSession
}

func (f *fakeSessionRunner) AgentName() string                { return f.agent }
func (f *fakeSessionRunner) Run(context.Context, Issue) error { return nil }
func (f *fakeSessionRunner) RunSession(ctx context.Context, _ Issue, session AgentSession, update func(AgentSession)) error {
	f.sessions <- session
	if f.reported.ID != "" || f.reported.CheckoutDirectory != "" {
		update(f.reported)
	}
	<-ctx.Done()
	return nil
}

type snapshotReporter struct {
	mu        sync.Mutex
	snapshots []GlorpSnapshot
}

func (r *snapshotReporter) Snapshot(snapshot GlorpSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, snapshot)
}

func (r *snapshotReporter) Log(string) {}

type fakeLabelEnsurer struct {
	called bool
	err    error
}

type fakeIssueLabeler struct {
	labels []bool
}

func (f *fakeIssueLabeler) EnsureLabels(_ context.Context, _ string) error {
	return nil
}

func (f *fakeIssueLabeler) SetIssueLabel(_ context.Context, _ string, _ int, add bool) error {
	f.labels = append(f.labels, add)
	return nil
}

type fakeIssueStatuser struct {
	mu       sync.Mutex
	statuses []string
	err      error
}

func (f *fakeIssueStatuser) SetIssueStatus(_ context.Context, _ string, _ Issue, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	return f.err
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
func TestGlorpRunsUnseenIssuesWithLimit(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 1}, {Number: 2}}, {{Number: 1}, {Number: 2}, {Number: 3}, {Number: 4}}}}
	r := &fakeRunner{release: make(chan struct{})}
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	w := &Glorp{Repo: "o/r", Interval: time.Millisecond, Concurrency: 2, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: &logs}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	close(r.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		dispatched := len(r.got)
		r.mu.Unlock()
		if dispatched == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	got := append([]int(nil), r.got...)
	max := r.max
	r.mu.Unlock()
	if len(got) != 4 || max > 2 {
		t.Fatalf("got=%v max=%d", got, max)
	}
	for _, want := range []string{"discovered 2 new issue(s)", "tasks: 2 running", "shutdown requested"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestGlorpPeriodicPollInterval(t *testing.T) {
	tests := []struct {
		name        string
		useWebhooks bool
		want        time.Duration
	}{
		{name: "poll mode", want: 12 * time.Second},
		{name: "push mode", useWebhooks: true, want: 90 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := &Glorp{Interval: 12 * time.Second, UseWebhooks: test.useWebhooks}
			if got := w.periodicPollInterval(); got != test.want {
				t.Fatalf("periodic poll interval = %s, want %s", got, test.want)
			}
		})
	}
}

func TestGlorpKeepsPollingProjectTargetsInWebhookMode(t *testing.T) {
	src := &fakeSource{batches: [][]Issue{{}}}
	w := &Glorp{
		Repo: "https://github.com/users/o/projects/3", Interval: time.Millisecond, Concurrency: 1,
		Issues: src, Runner: &fakeRunner{release: make(chan struct{})}, UseWebhooks: true,
		fallbackInterval: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 2 {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("project target did not receive a second poll")
}

func TestGlorpPeriodicPollResyncsRepositoryIssueInWebhookMode(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveWorkState(statePath, map[int]workState{7: {Status: "completed"}}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{batches: [][]Issue{
		{{Number: 7}},
		{{Number: 7, Labels: []IssueLabel{{Name: agentStartedLabel}}}},
	}}
	r := &fakeRunner{release: make(chan struct{})}
	w := &Glorp{
		Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: r, UseWebhooks: true,
		fallbackInterval: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := append([]int(nil), r.got...)
		r.mu.Unlock()
		if reflect.DeepEqual(got, []int{7}) {
			close(r.release)
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("periodic poll did not reclaim repository issue with agent-started label")
}

func TestGlorpShowsAgentOutputInJobSnapshot(t *testing.T) {
	reporter := &snapshotReporter{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 1, Title: "bug"}}}},
		Runner: fakeOutputRunner{}, UI: reporter,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	for _, snapshot := range reporter.snapshots {
		for _, job := range snapshot.Jobs {
			if job.Number == 1 && strings.Contains(job.Log, "agent output") {
				return
			}
		}
	}
	t.Fatalf("agent output was not included in snapshots: %+v", reporter.snapshots)
}

func TestGlorpPreservesAgentMetadataAfterCompletion(t *testing.T) {
	checkout := t.TempDir()
	reporter := &snapshotReporter{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 1, Title: "bug"}}}},
		Runner: fakeOutputRunner{reported: AgentSession{ID: "session-1", CheckoutDirectory: checkout}}, UI: reporter,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	for _, snapshot := range reporter.snapshots {
		for _, job := range snapshot.Jobs {
			if job.Number == 1 && job.Status == "complete" {
				if job.CheckoutDirectory != checkout || job.SessionID != "session-1" {
					t.Fatalf("completed job metadata was not preserved: %+v", job)
				}
				return
			}
		}
	}
	t.Fatalf("completed job snapshot was not published: %+v", reporter.snapshots)
}
func TestGlorpTreatsPreexistingUnseenIssuesAsNew(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	src := &fakeSource{batches: [][]Issue{{{Number: 1, CreatedAt: old}}}}
	r := &fakeRunner{release: make(chan struct{})}
	w := &Glorp{
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

func TestGlorpUpdatesProjectStatus(t *testing.T) {
	r := &fakeRunner{release: make(chan struct{})}
	status := &fakeIssueStatuser{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7, ProjectStatus: "Todo"}}}}, Runner: r, Status: status,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	close(r.release)
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status.mu.Lock()
	defer status.mu.Unlock()
	if got, want := status.statuses, []string{"In Progress", "Done"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("project statuses = %v, want %v", got, want)
	}
}

func TestGlorpDoesNotLabelProjectIssues(t *testing.T) {
	r := &fakeRunner{release: make(chan struct{})}
	labels := &fakeIssueLabeler{}
	status := &fakeIssueStatuser{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7, ProjectStatus: "Todo"}}}}, Runner: r, Labels: labels, Status: status,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	close(r.release)
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(labels.labels) != 0 {
		t.Fatalf("project issue labels = %v, want no label changes", labels.labels)
	}
	status.mu.Lock()
	defer status.mu.Unlock()
	if got, want := status.statuses, []string{"In Progress", "Done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project statuses = %v, want %v", got, want)
	}
}
func TestInvalidRepo(t *testing.T) {
	w := &Glorp{Repo: "bad", Interval: time.Second, Concurrency: 1}
	if w.Run(context.Background()) == nil {
		t.Fatal("expected error")
	}
}

func TestGlorpEnsuresLabelsOnStart(t *testing.T) {
	labels := &fakeLabelEnsurer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &Glorp{Repo: "o/r", Interval: time.Hour, Concurrency: 1, Labels: labels, Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})}}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !labels.called {
		t.Fatal("labels were not ensured on startup")
	}
}

func TestGlorpStopsWhenLabelEnsuringFails(t *testing.T) {
	labels := &fakeLabelEnsurer{err: context.Canceled}
	w := &Glorp{Repo: "o/r", Interval: time.Hour, Concurrency: 1, Labels: labels}
	if err := w.Run(context.Background()); err != context.Canceled {
		t.Fatalf("expected label error, got %v", err)
	}
}

func TestGlorpResetsFailedWorkOnStart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveWorkState(statePath, map[int]workState{7: {Status: "failed"}, 8: {Status: "completed"}}); err != nil {
		t.Fatal(err)
	}
	labels := &fakeIssueLabeler{}
	status := &fakeIssueStatuser{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})},
		Labels: labels, Status: status,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(labels.labels, []bool{false}) {
		t.Fatalf("labels = %v, want [false]", labels.labels)
	}
	status.mu.Lock()
	defer status.mu.Unlock()
	if !reflect.DeepEqual(status.statuses, []string{"Todo"}) {
		t.Fatalf("statuses = %v, want [Todo]", status.statuses)
	}
}

func TestGlorpResetsFailedProjectWorkOnStart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveWorkState(statePath, map[int]workState{7: {Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	labels := &fakeIssueLabeler{}
	status := &fakeIssueStatuser{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})},
		Labels: labels, Status: status, ReadyState: "Agent Queue",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(labels.labels) != 0 {
		t.Fatalf("project labels = %v, want no changes", labels.labels)
	}
	status.mu.Lock()
	defer status.mu.Unlock()
	if !reflect.DeepEqual(status.statuses, []string{"Agent Queue"}) {
		t.Fatalf("statuses = %v, want [Agent Queue]", status.statuses)
	}
}

func TestGlorpIgnoresMissingProjectIssueWhenResettingFailedWork(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveWorkState(statePath, map[int]workState{7: {Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	status := &fakeIssueStatuser{err: errProjectIssueNotFound}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})}, Status: status,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("missing project issue should not stop glorp: %v", err)
	}
}

func TestGlorpKeepsWatchingWhenProjectResetFails(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveWorkState(statePath, map[int]workState{7: {Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	status := &fakeIssueStatuser{err: errors.New("list project items: exit status 1")}
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})}, Status: status, Out: &logs,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("project reset failure should not stop glorp: %v", err)
	}
	if !strings.Contains(logs.String(), "reset failed issue #7 project status: list project items: exit status 1") {
		t.Fatalf("project reset failure was not logged:\n%s", logs.String())
	}
}

func TestCommandRunnerUsesSelectedAgentSyntax(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	if got, want := commandArgs(CommandRunner{Agent: "codex"}, Issue{Number: 12}), []string{"exec", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codex args: %#v", got)
	}
	if got, want := commandArgs(CommandRunner{Agent: "claude"}, Issue{Number: 12}), []string{"-p", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude args: %#v", got)
	}
}

func TestCommandRunnerIncludesIssueRepository(t *testing.T) {
	prompt := "/gh-fix 12\n\nRepository: owner/repo\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	issue := Issue{Number: 12, Repository: "owner/repo", Target: "https://github.com/users/owner/projects/3"}
	got := commandArgs(CommandRunner{Agent: "codex", Repo: "wrong/repo"}, issue)
	want := []string{"exec", prompt}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codex args = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerYoloDisablesAgentSafetyChecks(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	if got, want := commandArgs(CommandRunner{Agent: "codex", Yolo: true}, Issue{Number: 12}), []string{"exec", "--dangerously-bypass-approvals-and-sandbox", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codex yolo args = %#v, want %#v", got, want)
	}
	if got, want := commandArgs(CommandRunner{Agent: "claude", Yolo: true}, Issue{Number: 12}), []string{"-p", "--dangerously-skip-permissions", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude yolo args = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerPassesModelAndLevel(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	if got, want := commandArgs(CommandRunner{Agent: "codex", Model: "gpt-5.6-luna", ModelLevel: "high"}, Issue{Number: 12}), []string{"exec", "--model", "gpt-5.6-luna", "-c", "model_reasoning_effort=high", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codex args = %#v, want %#v", got, want)
	}
	if got, want := commandArgs(CommandRunner{Agent: "claude", Model: "claude-sonnet", ModelLevel: "medium"}, Issue{Number: 12}), []string{"-p", "--model", "claude-sonnet", "--effort", "medium", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude args = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerResumesOriginalAgentSession(t *testing.T) {
	dir := t.TempDir()
	codex := CommandRunner{Agent: "claude", Yolo: true, Model: "saved-model", ModelLevel: "high"}
	session := AgentSession{ID: "session-7", Agent: "codex", CheckoutDirectory: dir, Resume: true}
	wantCodex := []string{"exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "session-7", "continue"}
	if got := commandArgsForSession(codex, Issue{Number: 7}, session); !reflect.DeepEqual(got, wantCodex) {
		t.Fatalf("Codex resume args = %#v, want %#v", got, wantCodex)
	}

	claude := CommandRunner{Agent: "codex", Yolo: true, Model: "saved-model", ModelLevel: "medium"}
	session.Agent = "claude"
	wantClaude := []string{"-p", "--resume", "session-7", "--dangerously-skip-permissions", "continue"}
	if got := commandArgsForSession(claude, Issue{Number: 7}, session); !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("Claude resume args = %#v, want %#v", got, wantClaude)
	}
}

func TestCommandRunnerStartsClaudeWithPersistedSessionID(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	session := AgentSession{ID: "session-12", Agent: "claude"}
	want := []string{"-p", "--session-id", "session-12", prompt}
	if got := commandArgsForSession(CommandRunner{Agent: "codex"}, Issue{Number: 12}, session); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude initial args = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerRegeneratesWorkWhenCheckoutIsMissing(t *testing.T) {
	session := AgentSession{ID: "session-7", Agent: "codex", CheckoutDirectory: filepath.Join(t.TempDir(), "missing"), Resume: true}
	args := commandArgsForSession(CommandRunner{Agent: "claude"}, Issue{Number: 7}, session)
	prompt := args[len(args)-1]
	if !strings.HasPrefix(prompt, "continue") || !strings.Contains(prompt, "repository directory no longer exists") || !strings.Contains(prompt, "Regenerate") {
		t.Fatalf("missing-checkout prompt = %q", prompt)
	}
}

func TestSessionMetadataCaptureWriterPreservesOutputAndCapturesCodexSessionAndCheckout(t *testing.T) {
	checkout := t.TempDir()
	var output bytes.Buffer
	var updates []AgentSession
	w := &sessionMetadataCaptureWriter{
		output: &output, captureSession: true,
		onUpdate: func(update AgentSession) { updates = append(updates, update) },
	}
	chunks := []string{
		"OpenAI Codex\nsession ",
		"id: 0199a213-81c0-7800-8aa1-bbab2a035a53\nworking\nGLORP_CHECKOUT_",
		"DIRECTORY=" + checkout + "\n",
	}
	for _, chunk := range chunks {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), strings.Join(chunks, ""); got != want {
		t.Fatalf("forwarded output = %q, want %q", got, want)
	}
	w.Flush()
	want := []AgentSession{{ID: "0199a213-81c0-7800-8aa1-bbab2a035a53"}, {CheckoutDirectory: checkout}}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("captured metadata = %#v, want %#v", updates, want)
	}
}

func TestSessionMetadataCaptureWriterIgnoresInvalidCheckout(t *testing.T) {
	var updates []AgentSession
	w := &sessionMetadataCaptureWriter{
		output:   io.Discard,
		onUpdate: func(update AgentSession) { updates = append(updates, update) },
	}
	_, _ = io.WriteString(w, "GLORP_CHECKOUT_DIRECTORY=relative/path\nGLORP_CHECKOUT_DIRECTORY="+filepath.Join(t.TempDir(), "missing")+"\n")
	if len(updates) != 0 {
		t.Fatalf("invalid checkout metadata was captured: %#v", updates)
	}
}

func TestCommandRunnerUsesTerminalAgentStdin(t *testing.T) {
	cmd := newAgentCommand(context.Background(), "test-agent")
	terminal := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
	if terminal && cmd.Stdin != os.Stdin {
		t.Fatal("agent stdin must use the terminal in interactive mode")
	}
	if !terminal && cmd.Stdin != nil {
		t.Fatal("agent stdin must use the null device in headless mode")
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
	want := map[int]workState{7: {Status: "active", SessionID: "session-7", Agent: "codex", CheckoutDirectory: "/tmp/repo"}}
	if err := saveWorkState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadWorkState(path)
	if err != nil || got[7] != want[7] {
		t.Fatalf("state error=%v value=%v", err, got)
	}
}

func TestGlorpResumesPersistedSessionWithOriginalAgent(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	want := workState{Status: "active", SessionID: "session-7", Agent: "codex", CheckoutDirectory: dir}
	if err := saveWorkState(statePath, map[int]workState{7: want}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeSessionRunner{agent: "claude", sessions: make(chan AgentSession, 1)}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7, Labels: []IssueLabel{{Name: agentStartedLabel}}}}}}, Runner: runner,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case got := <-runner.sessions:
		if !got.Resume || got.ID != want.SessionID || got.Agent != want.Agent || got.CheckoutDirectory != want.CheckoutDirectory {
			t.Fatalf("resumed session = %#v, want persisted %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted session was not resumed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpPersistsSessionReportedByCodex(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	checkout := filepath.Join(dir, "glorp-gh-fix-7")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeSessionRunner{agent: "codex", sessions: make(chan AgentSession, 1), reported: AgentSession{ID: "codex-session", CheckoutDirectory: checkout}}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7}}}}, Runner: runner,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case session := <-runner.sessions:
		if session.Resume || session.ID != "" || session.Agent != "codex" || session.CheckoutDirectory != "" {
			t.Fatalf("new Codex session = %#v", session)
		}
	case <-time.After(time.Second):
		t.Fatal("new Codex session was not started")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := loadWorkState(statePath)
		if err == nil && state[7].SessionID == "codex-session" && state[7].Agent == "codex" && state[7].CheckoutDirectory == checkout {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("Codex session ID was not persisted")
}

func TestScopedWorkStateKeepsTargetsSeparate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	targets := []string{"o/one", "o/two"}
	want := map[string]workState{
		"o/one#7": {Status: "completed", SessionID: "one"},
		"o/two#7": {Status: "active", SessionID: "two"},
	}
	if err := saveScopedWorkState(path, want, targets); err != nil {
		t.Fatal(err)
	}
	got, err := loadScopedWorkState(path, targets)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped state error=%v value=%v, want %v", err, got, want)
	}
}

func TestGlorpKeepsWebhookFollowUpWhenAnotherDeliveryArrives(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{
		{},                         // initial baseline
		{},                         // first webhook arrives before issue indexing catches up
		{{Number: 1}},              // second webhook observes the previous issue
		{{Number: 1}, {Number: 2}}, // preserved follow-up observes the latest issue
	}}
	r := &fakeRunner{release: make(chan struct{})}
	events := make(chan WebhookEvent, 2)
	w := &Glorp{
		Repo: "o/r", Interval: 40 * time.Millisecond, Concurrency: 2,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: r,
		UseWebhooks: true, Events: events,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	events <- WebhookEvent{Kind: "issues", Action: "opened", IssueNumber: 1}
	time.Sleep(10 * time.Millisecond)
	events <- WebhookEvent{Kind: "issues", Action: "opened", IssueNumber: 2}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := append([]int(nil), r.got...)
		r.mu.Unlock()
		if len(got) >= 2 {
			if !reflect.DeepEqual(got, []int{1, 2}) {
				t.Fatalf("runner received issues %v, want [1 2]", got)
			}
			close(r.release)
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("webhook follow-up did not dispatch the latest issue")
}

func TestGlorpReloadsChangedStateAfterDebounce(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveWorkState(statePath, map[int]workState{1: {Status: "completed"}}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{batches: [][]Issue{{{Number: 1}}, {{Number: 1}, {Number: 2}}}}
	r := &fakeRunner{release: make(chan struct{})}
	w := &Glorp{Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath, Issues: src, Runner: r}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Let the initial poll finish persisting its baseline before editing it.
	time.Sleep(200 * time.Millisecond)
	if err := saveWorkState(statePath, map[int]workState{}); err != nil {
		t.Fatal(err)
	}
	released := false
	deadline = time.Now().Add(stateReloadDebounce + 2*time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := append([]int(nil), r.got...)
		r.mu.Unlock()
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 2 && len(got) == 1 && !released {
			close(r.release)
			released = true
		}
		if len(got) == 2 {
			if !reflect.DeepEqual(got, []int{1, 2}) {
				t.Fatalf("dispatched issues = %v, want [1 2]", got)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("changed state was not reloaded")
}

func TestIssueKeyUsesTargetAndNumber(t *testing.T) {
	if got := issueKey(Issue{Target: "o/one", Number: 7}); got != "o/one#7" {
		t.Fatalf("issue key = %q", got)
	}
	if got := issueKey(Issue{Repository: "o/two", Number: 7}); got != "o/two#7" {
		t.Fatalf("repository fallback issue key = %q", got)
	}
}

func TestGlorpPersistsSessionIDAfterCompletion(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}}}
	r := &fakeRunner{release: make(chan struct{})}
	w := &Glorp{
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

func TestShouldDispatchIssueUsesProjectStatusForRecovery(t *testing.T) {
	project := "https://github.com/users/lsegal/projects/3"
	if shouldDispatchIssue(project, Issue{ProjectStatus: "In Progress"}, false, false, false, "") {
		t.Fatal("new in-progress project issue was dispatched")
	}
	for _, status := range []string{"Done", "Completed"} {
		if shouldDispatchIssue(project, Issue{ProjectStatus: status}, false, false, false, "") {
			t.Fatalf("new %s project issue was dispatched", status)
		}
	}
	for _, status := range []string{"Todo", "TODO", "Ready", "ready"} {
		if !shouldDispatchIssue(project, Issue{ProjectStatus: status}, false, false, false, "") {
			t.Fatalf("new %s project issue was not dispatched", status)
		}
	}
	if shouldDispatchIssue(project, Issue{ProjectStatus: "Backlog"}, false, false, false, "") {
		t.Fatal("new backlog project issue was dispatched")
	}
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "Agent Queue"}, false, false, false, "agent queue") {
		t.Fatal("configured ready project issue was not dispatched")
	}
	if shouldDispatchIssue(project, Issue{ProjectStatus: "Ready"}, false, false, false, "Agent Queue") {
		t.Fatal("default ready status was used despite configured ready state")
	}
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "In Progress"}, false, false, true, "") {
		t.Fatal("in-progress project issue was not reclaimed")
	}
	if shouldDispatchIssue(project, Issue{ProjectStatus: "Todo"}, false, false, true, "") {
		t.Fatal("non-in-progress project issue was reclaimed")
	}
	if !shouldDispatchIssue("o/r", Issue{Labels: []IssueLabel{{Name: agentStartedLabel}}}, false, false, true, "") {
		t.Fatal("agent-started repository issue was not reclaimed")
	}
}

func TestRestoredWorkStateMatchesRemote(t *testing.T) {
	project := "https://github.com/users/lsegal/projects/3"
	tests := []struct {
		name   string
		target string
		issue  Issue
		state  workState
		want   bool
	}{
		{name: "repository active matches label", target: "o/r", issue: Issue{Labels: []IssueLabel{{Name: agentStartedLabel}}}, state: workState{Status: "active"}, want: true},
		{name: "repository active missing label", target: "o/r", state: workState{Status: "active"}},
		{name: "repository completed issue is open", target: "o/r", state: workState{Status: "completed"}},
		{name: "project active matches status", target: project, issue: Issue{ProjectStatus: "In Progress"}, state: workState{Status: "active"}, want: true},
		{name: "project active reset to ready", target: project, issue: Issue{ProjectStatus: "Ready"}, state: workState{Status: "active"}},
		{name: "project completed matches done", target: project, issue: Issue{ProjectStatus: "Done"}, state: workState{Status: "completed"}, want: true},
		{name: "project completed reset to ready", target: project, issue: Issue{ProjectStatus: "Todo"}, state: workState{Status: "completed"}},
		{name: "failed state is not reconciled", target: "o/r", state: workState{Status: "failed"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := workStateMatchesRemote(test.target, test.issue, test.state); got != test.want {
				t.Fatalf("workStateMatchesRemote() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGlorpRequeuesStaleRepositoryWorkState(t *testing.T) {
	for _, status := range []string{"active", "completed"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			statePath := filepath.Join(dir, "state.json")
			if err := saveWorkState(statePath, map[int]workState{7: {Status: status, SessionID: "old"}}); err != nil {
				t.Fatal(err)
			}
			r := &fakeRunner{release: make(chan struct{})}
			var logs bytes.Buffer
			w := &Glorp{
				Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
				Issues: &fakeSource{batches: [][]Issue{{{Number: 7}}}}, Runner: r, Out: &logs,
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- w.Run(ctx) }()

			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				r.mu.Lock()
				dispatched := append([]int(nil), r.got...)
				r.mu.Unlock()
				if reflect.DeepEqual(dispatched, []int{7}) {
					close(r.release)
					cancel()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if want := "reset stale local " + status + " state"; !strings.Contains(logs.String(), want) {
						t.Fatalf("logs did not report %s reset:\n%s", status, logs.String())
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			close(r.release)
			<-done
			t.Fatalf("open repository issue with %s local state was not requeued", status)
		})
	}
}

func TestProjectReadyState(t *testing.T) {
	for _, test := range []struct {
		configured string
		current    string
		want       string
	}{
		{configured: " Agent Queue ", current: "Ready", want: "Agent Queue"},
		{current: "ready", want: "ready"},
		{current: "Backlog", want: "Todo"},
	} {
		if got := projectReadyState(test.configured, test.current); got != test.want {
			t.Errorf("projectReadyState(%q, %q) = %q, want %q", test.configured, test.current, got, test.want)
		}
	}
}

func TestIssueBlockedUntilDependenciesClose(t *testing.T) {
	blocked, reason := issueBlocked(Issue{DependsOn: []IssueDependency{{Number: 4, State: "open"}, {Number: 7, State: "CLOSED"}}})
	if !blocked || reason != "depends on #4 (open)" {
		t.Fatalf("blocked=%v reason=%q", blocked, reason)
	}
	if blocked, _ := issueBlocked(Issue{DependsOn: []IssueDependency{{Number: 7, State: "closed"}}}); blocked {
		t.Fatal("closed dependency still blocks issue")
	}
}
