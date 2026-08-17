package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-isatty"
)

type fakeSource struct {
	mu      sync.Mutex
	calls   int
	batches [][]Issue
}

type fakeClosureSource struct {
	*fakeSource
	mu    sync.Mutex
	state OriginatingWorkState
	err   error
}

func (f *fakeClosureSource) OriginatingWorkState(_ context.Context, _ string, _ int) (OriginatingWorkState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.err
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

type fakeDiscussionSource struct {
	mu      sync.Mutex
	calls   int
	batches [][]Discussion
}

func (f *fakeDiscussionSource) ListUnansweredDiscussions(_ context.Context, _ string) ([]Discussion, error) {
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
	// dispatched, when non-nil, receives the issue number as each Run call
	// starts, letting tests synchronize on dispatch instead of polling got.
	dispatched chan int
	// allow, when non-nil, gates each Run call individually: it must receive
	// a value before that call returns, instead of the shared release
	// channel unblocking every call (including future ones) at once.
	allow chan struct{}
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
	if f.dispatched != nil {
		select {
		case f.dispatched <- i.Number:
		case <-ctx.Done():
		}
	}
	if f.allow != nil {
		select {
		case <-f.allow:
		case <-ctx.Done():
		}
	} else {
		select {
		case <-f.release:
		case <-ctx.Done():
		}
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

func TestGlorpDispatchesUnansweredDiscussions(t *testing.T) {
	dir := t.TempDir()
	target := "https://github.com/o/r/discussions"
	ds := &fakeDiscussionSource{batches: [][]Discussion{{{Number: 9, Title: "Q"}}}}
	src := &fakeSource{batches: [][]Issue{{}}}
	r := &fakeRunner{release: make(chan struct{})}
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	w := &Glorp{Repo: target, Interval: time.Millisecond, Concurrency: 2, StatePath: filepath.Join(dir, "state"), Issues: src, Discussions: ds, Runner: r, Out: &logs}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		dispatched := len(r.got)
		r.mu.Unlock()
		if dispatched == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(r.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	got := append([]int(nil), r.got...)
	r.mu.Unlock()
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("got=%v", got)
	}
	src.mu.Lock()
	issueCalls := src.calls
	src.mu.Unlock()
	if issueCalls != 0 {
		t.Fatalf("issue source should not be polled for a Discussions-board target, calls=%d", issueCalls)
	}
	if !strings.Contains(logs.String(), "discussion #9 completed") {
		t.Errorf("logs missing discussion completion:\n%s", logs.String())
	}
}

func TestGlorpSkipsLabelEnsuringForDiscussionTargets(t *testing.T) {
	labels := &fakeLabelEnsurer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &Glorp{Repo: "https://github.com/o/r/discussions", Interval: time.Hour, Concurrency: 1, Labels: labels, Discussions: &fakeDiscussionSource{batches: [][]Discussion{{}}}, Runner: &fakeRunner{release: make(chan struct{})}}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if labels.called {
		t.Fatal("labels should not be ensured for a Discussions-board target")
	}
}

func TestGlorpRetriesWebhookRefreshUntilIssueIsIndexed(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{
		{},            // initial poll
		{},            // webhook-triggered poll
		{},            // first retry
		{},            // second retry
		{{Number: 7}}, // delayed GitHub issue index
	}}
	runner := &fakeRunner{release: make(chan struct{})}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, UseWebhooks: true, Events: events,
		fallbackInterval: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
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
	events <- WebhookEvent{Kind: "issues", Action: "opened", IssueNumber: 7}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		got := append([]int(nil), runner.got...)
		runner.mu.Unlock()
		if len(got) > 0 {
			if got[0] != 7 {
				t.Fatalf("runner received issue #%d, want #7", got[0])
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	runner.mu.Lock()
	dispatched := len(runner.got) > 0
	runner.mu.Unlock()
	if !dispatched {
		t.Fatal("webhook retries did not dispatch the delayed issue")
	}
	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpRespondsImmediatelyToOwnershipAskWebhook(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	events := make(chan WebhookEvent, 1)
	comments := newFakeCommentClient()
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, UseWebhooks: true, Events: events,
		fallbackInterval: time.Hour, Identity: "SELF", Comments: comments,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case n := <-runner.dispatched:
		if n != 7 {
			t.Fatalf("dispatched issue #%d, want #7", n)
		}
	case <-time.After(time.Second):
		t.Fatal("issue #7 was not dispatched")
	}

	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 7, CommentBody: signComment(askClaimBody, "OTHER")}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		comments.mu.Lock()
		posts := comments.posts
		comments.mu.Unlock()
		if posts > 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	posted, err := comments.ListComments(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) != 2 {
		t.Fatalf("posted comments = %v, want the starting claim plus an immediate presence reply", posted)
	}
	last := posted[len(posted)-1]
	if kind, id, ok := parseClaim(last.Body); !ok || kind != claimPresence || id != "SELF" {
		t.Fatalf("posted comment = %q", last.Body)
	}

	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpStopsAgentWhenOriginatingWorkCloses(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeClosureSource{fakeSource: &fakeSource{batches: [][]Issue{{{Number: 7}}}}}
	runner := &fakeRunner{release: make(chan struct{})}
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: &logs, closureInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		started := len(runner.got) == 1
		runner.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	src.mu.Lock()
	src.state = OriginatingWorkState{IssueState: "OPEN", PullRequests: []PullRequestWorkState{{Number: 9, State: "CLOSED"}}}
	src.mu.Unlock()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := loadWorkState(statePath)
		if err == nil && state[7].Status == "failed" {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), "stopping agent: pull request #9 was closed without merging") || !strings.Contains(logs.String(), "work closed by user") {
				t.Fatalf("closure failure was not logged:\n%s", logs.String())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("closed originating work did not stop the agent:\n%s", logs.String())
}

func TestGlorpDoesNotCancelRunWithoutCompetingClaim(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}}}
	runner := &fakeSessionRunner{agent: "codex", sessions: make(chan AgentSession, 1)}
	comments := newFakeCommentClient()
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: &logs, closureInterval: time.Millisecond,
		Comments: comments, Identity: "SELF",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	<-runner.sessions

	// Let several competing-claim ticks elapse with no comments posted.
	time.Sleep(50 * time.Millisecond)

	state, err := loadWorkState(statePath)
	if err != nil || state[7].Status != "active" {
		cancel()
		<-done
		t.Fatalf("expected issue to remain active with no competing claim, state=%v err=%v", state, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpCancelsRunAndRemovesCheckoutWhenAnotherInstanceClaimsIssue(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	checkoutDir := filepath.Join(dir, "checkout")
	if err := os.MkdirAll(checkoutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}}}
	runner := &fakeSessionRunner{agent: "codex", sessions: make(chan AgentSession, 1), reported: AgentSession{CheckoutDirectory: checkoutDir}}
	comments := newFakeCommentClient()
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: &logs, closureInterval: time.Millisecond,
		Comments: comments, Identity: "SELF",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	<-runner.sessions

	comments.inject("o/r", 7, Comment{Body: signComment(startingClaimBody, "OTHER"), CreatedAt: time.Now()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := loadWorkState(statePath)
		if err == nil && state[7].Status == "failed" {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Stat(checkoutDir); !os.IsNotExist(statErr) {
				t.Fatalf("expected checkout directory to be removed, stat err = %v", statErr)
			}
			if !strings.Contains(logs.String(), "claimed by another instance") {
				t.Fatalf("competing claim was not logged:\n%s", logs.String())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("competing claim did not stop the agent:\n%s", logs.String())
}

func TestGlorpIgnoresCompetingClaimFromSameIdentity(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}}}
	runner := &fakeSessionRunner{agent: "codex", sessions: make(chan AgentSession, 1)}
	comments := newFakeCommentClient()
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: &logs, closureInterval: time.Millisecond,
		Comments: comments, Identity: "SELF",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	<-runner.sessions

	comments.inject("o/r", 7, Comment{Body: signComment(startingClaimBody, "SELF"), CreatedAt: time.Now()})

	// Let several competing-claim ticks elapse; the self-signed claim must
	// not be mistaken for another instance taking over.
	time.Sleep(50 * time.Millisecond)

	state, err := loadWorkState(statePath)
	if err != nil || state[7].Status != "active" {
		cancel()
		<-done
		t.Fatalf("expected issue to remain active despite own-identity claim, state=%v err=%v", state, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClosedWorkReasonIgnoresPullRequestsAlreadyClosedAtStart(t *testing.T) {
	closed := PullRequestWorkState{Number: 8, State: "CLOSED"}
	previous := OriginatingWorkState{IssueState: "OPEN", PullRequests: []PullRequestWorkState{closed}}
	current := OriginatingWorkState{IssueState: "OPEN", PullRequests: []PullRequestWorkState{closed, {Number: 9, State: "OPEN"}}}
	if reason := closedWorkReason(previous, current, 7); reason != "" {
		t.Fatalf("preexisting closed pull request stopped work: %s", reason)
	}
	current.PullRequests[1].State = "CLOSED"
	if reason := closedWorkReason(previous, current, 7); reason != "pull request #9 was closed without merging" {
		t.Fatalf("newly closed pull request reason = %q", reason)
	}
}

func TestGlorpPeriodicPollInterval(t *testing.T) {
	tests := []struct {
		name        string
		useWebhooks bool
		want        time.Duration
	}{
		{name: "poll mode", want: 12 * time.Second},
		{name: "push mode", useWebhooks: true, want: 15 * time.Minute},
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
		{{Number: 7}},
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
	t.Fatal("periodic poll did not resync stale completed repository issue")
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		dispatched := len(r.got) > 0
		r.mu.Unlock()
		if dispatched || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
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

// statusFailingOnce fails SetIssueStatus exactly once (for whichever issue
// asks first), then succeeds for every subsequent call.
type statusFailingOnce struct {
	mu      sync.Mutex
	failed  bool
	numbers []int
}

func (f *statusFailingOnce) SetIssueStatus(_ context.Context, _ string, issue Issue, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if status == "In Progress" && !f.failed {
		f.failed = true
		return errors.New("transient status update failure")
	}
	f.numbers = append(f.numbers, issue.Number)
	return nil
}

func TestGlorpDispatchSkipsIssueOnStatusUpdateFailureWithoutAbortingOthers(t *testing.T) {
	r := &fakeRunner{release: make(chan struct{})}
	status := &statusFailingOnce{}
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 2,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7}, {Number: 8}}}}, Runner: r, Status: status, Out: &logs,
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
	r.mu.Lock()
	got := append([]int(nil), r.got...)
	r.mu.Unlock()
	if len(got) != 1 || got[0] != 8 {
		t.Fatalf("dispatched issues = %v, want only #8 (the one whose status update succeeded)", got)
	}
	if !strings.Contains(logs.String(), "issue #7 not dispatched; failed to set project status") {
		t.Fatalf("logs = %q, want a not-dispatched message for issue #7", logs.String())
	}
}

// erroringThenSucceedingSource fails ListIssues for its first failN calls,
// then serves batches normally.
type erroringThenSucceedingSource struct {
	mu      sync.Mutex
	calls   int
	failN   int
	batches [][]Issue
}

func (f *erroringThenSucceedingSource) ListIssues(_ context.Context, _ string) ([]Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.calls
	f.calls++
	if n < f.failN {
		return nil, errors.New("transient listing failure")
	}
	idx := n - f.failN
	if idx >= len(f.batches) {
		idx = len(f.batches) - 1
	}
	return f.batches[idx], nil
}

func TestGlorpInitialPollFailureIsNotFatal(t *testing.T) {
	src := &erroringThenSucceedingSource{failN: 1, batches: [][]Issue{{{Number: 7}}}}
	r := &fakeRunner{release: make(chan struct{})}
	var logs bytes.Buffer
	w := &Glorp{Repo: "o/r", Interval: 10 * time.Millisecond, Concurrency: 1, Issues: src, Runner: r, Out: &logs}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		got := len(r.got)
		r.mu.Unlock()
		if got > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("issue was never dispatched after the initial poll failure")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(r.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "initial poll error") {
		t.Fatalf("logs = %q, want an initial poll error message", logs.String())
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
	status := &fakeIssueStatuser{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})},
		Status: status,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
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
	status := &fakeIssueStatuser{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{}}}, Runner: &fakeRunner{release: make(chan struct{})},
		Status: status, ReadyState: "Agent Queue",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	status.mu.Lock()
	defer status.mu.Unlock()
	if !reflect.DeepEqual(status.statuses, []string{"Agent Queue"}) {
		t.Fatalf("statuses = %v, want [Agent Queue]", status.statuses)
	}
}

// A project item left at "In Progress" by an instance that died mid-run is
// invisible to the new instance's local state, so it must be reclaimed
// through the handoff handshake rather than skipped forever (issue #231).
func TestGlorpReclaimsStrandedInProgressProjectItem(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7, Repository: "o/r", ProjectStatus: "In Progress"}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	comments := newFakeCommentClient()
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: runner, Out: &logs,
		Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case got := <-runner.dispatched:
		if got != 7 {
			t.Fatalf("dispatched issue #%d, want #7", got)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatalf("stranded in-progress project item was never reclaimed:\n%s", logs.String())
	}

	posted, err := comments.ListComments(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) != 2 {
		t.Fatalf("posted comments = %v, want an ask followed by a claim", posted)
	}
	if kind, id, ok := parseClaim(posted[0].Body); !ok || kind != claimAsking || id != "SELF" {
		t.Fatalf("first comment = %q, want the ownership ask", posted[0].Body)
	}
	if kind, id, ok := parseClaim(posted[1].Body); !ok || kind != claimStarting || id != "SELF" {
		t.Fatalf("second comment = %q, want our starting claim", posted[1].Body)
	}

	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The reclaim above must still respect a live owner: when another instance
// answers the ask, this one stands down and leaves the item alone.
func TestGlorpStandsDownOnStrandedProjectItemStillOwned(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7, Repository: "o/r", ProjectStatus: "In Progress"}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	comments := newFakeCommentClient()
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: runner, Out: &logs,
		Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool {
			comments.inject("o/r", 7, Comment{Body: signComment(presenceClaimBody, "OTHER"), CreatedAt: time.Now()})
			return true
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case got := <-runner.dispatched:
		cancel()
		<-done
		t.Fatalf("issue #%d was dispatched despite another instance owning it", got)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "ownership claimed by another instance; standing down") {
		t.Fatalf("standing down was not logged:\n%s", logs.String())
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
	if got, want := commandArgs(CommandRunner{Agent: "claude"}, Issue{Number: 12}), []string{"-p", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", prompt}; !reflect.DeepEqual(got, want) {
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

func TestCommandRunnerUsesGhDiscussForDiscussionTargets(t *testing.T) {
	prompt := "/gh-discuss 5\n\nRepository: owner/repo\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	issue := Issue{Number: 5, Target: "https://github.com/owner/repo/discussions"}
	got := commandArgs(CommandRunner{Agent: "codex"}, issue)
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
	if got, want := commandArgs(CommandRunner{Agent: "claude", Yolo: true}, Issue{Number: 12}), []string{"-p", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude yolo args = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerPassesAgentSpecModelAndLevel(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	if got, want := commandArgs(CommandRunner{Agent: "codex/gpt-5.6-luna:high"}, Issue{Number: 12}), []string{"exec", "--model", "gpt-5.6-luna", "-c", "model_reasoning_effort=high", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codex args = %#v, want %#v", got, want)
	}
	if got, want := commandArgs(CommandRunner{Agent: "claude/claude-sonnet:medium"}, Issue{Number: 12}), []string{"-p", "--permission-mode", "auto", "--model", "claude-sonnet", "--effort", "medium", "--output-format", "stream-json", "--verbose", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude args = %#v, want %#v", got, want)
	}
	if got, want := commandArgs(CommandRunner{Agent: "codex:low"}, Issue{Number: 12}), []string{"exec", "-c", "model_reasoning_effort=low", prompt}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codex level-only args = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerUsesSessionAgentSpecModel(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	runner := CommandRunner{Agents: []string{"codex/gpt-5.6-luna:high", "claude/opus:low"}, Agent: "codex/gpt-5.6-luna:high"}
	session := AgentSession{ID: "session-12", Agent: "claude/opus:low"}
	want := []string{"-p", "--session-id", "session-12", "--permission-mode", "auto", "--model", "opus", "--effort", "low", "--output-format", "stream-json", "--verbose", prompt}
	if got := commandArgsForSession(runner, Issue{Number: 12}, session); !reflect.DeepEqual(got, want) {
		t.Fatalf("session spec args = %#v, want %#v", got, want)
	}
	runner.ClaudeBinary = "claude-bin"
	if got, want := runner.binary(runner.specForSession(session).Name), "claude-bin"; got != want {
		t.Fatalf("binary for spec = %q, want %q", got, want)
	}
}

func TestParseAgentSpec(t *testing.T) {
	for _, test := range []struct {
		value string
		want  agentSpec
	}{
		{"codex", agentSpec{Name: "codex"}},
		{" claude ", agentSpec{Name: "claude"}},
		{"claude/opus", agentSpec{Name: "claude", Model: "opus"}},
		{"codex:high", agentSpec{Name: "codex", Level: "high"}},
		{"codex/gpt-5.6:medium", agentSpec{Name: "codex", Model: "gpt-5.6", Level: "medium"}},
		{"claude/anthropic/claude-opus:low", agentSpec{Name: "claude", Model: "anthropic/claude-opus", Level: "low"}},
	} {
		got, err := parseAgentSpec(test.value)
		if err != nil {
			t.Fatalf("parseAgentSpec(%q) error = %v", test.value, err)
		}
		if got != test.want {
			t.Fatalf("parseAgentSpec(%q) = %#v, want %#v", test.value, got, test.want)
		}
		if roundTrip := got.String(); roundTrip != strings.TrimSpace(test.value) && test.value != " claude " {
			t.Fatalf("agentSpec(%q).String() = %q", test.value, roundTrip)
		}
	}
	for _, value := range []string{"gemini", "codex/gpt-5:turbo", "claude/", "codex/gpt-5:", ""} {
		if _, err := parseAgentSpec(value); err == nil {
			t.Fatalf("parseAgentSpec(%q) error = nil, want error", value)
		}
	}
}

func TestAgentFlagCollectsSpecs(t *testing.T) {
	flags := agentFlag{values: []agentSpec{{Name: "codex"}}}
	if err := flags.Set("claude/opus:high"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := flags.Set("codex/gpt-5.6"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if got, want := flags.specs(), []string{"claude/opus:high", "codex/gpt-5.6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("specs() = %#v, want %#v", got, want)
	}
	if got, want := flags.names(), []string{"claude", "codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names() = %#v, want %#v", got, want)
	}
	if err := flags.Set("gemini"); err == nil {
		t.Fatal("Set(\"gemini\") error = nil, want error")
	}
}

func TestCommandRunnerResumesOriginalAgentSession(t *testing.T) {
	dir := t.TempDir()
	codex := CommandRunner{Agent: "claude/saved-model:high", Yolo: true}
	session := AgentSession{ID: "session-7", Agent: "codex", CheckoutDirectory: dir, Resume: true}
	resumePrompt := "continue\n\nRecover the existing work. If this issue has a draft pull request, inspect it and pull its branch before continuing."
	wantCodex := []string{"exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "session-7", resumePrompt}
	if got := commandArgsForSession(codex, Issue{Number: 7}, session); !reflect.DeepEqual(got, wantCodex) {
		t.Fatalf("Codex resume args = %#v, want %#v", got, wantCodex)
	}

	claude := CommandRunner{Agent: "codex/saved-model:medium", Yolo: true}
	session.Agent = "claude"
	wantClaude := []string{"-p", "--resume", "session-7", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose", resumePrompt}
	if got := commandArgsForSession(claude, Issue{Number: 7}, session); !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("Claude resume args = %#v, want %#v", got, wantClaude)
	}
}

func TestCommandRunnerStartsClaudeWithPersistedSessionID(t *testing.T) {
	prompt := "/gh-fix 12\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	session := AgentSession{ID: "session-12", Agent: "claude"}
	want := []string{"-p", "--session-id", "session-12", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", prompt}
	if got := commandArgsForSession(CommandRunner{Agent: "codex"}, Issue{Number: 12}, session); !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude initial args = %#v, want %#v", got, want)
	}
}

// writeFakeAgent installs an executable stub that appends each invocation's
// arguments to a log file and emits the supplied lines, exiting with code when
// the invocation is a resume.
func writeFakeAgent(t *testing.T, resumeOutput string, resumeCode int) (binary, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake agent script requires a POSIX shell")
	}
	dir := t.TempDir()
	binary, log = filepath.Join(dir, "agent.sh"), filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\n{ echo \"$@\"; echo '<<<END>>>'; } >> " + log + "\nfor arg in \"$@\"; do\n" +
		"  case \"$arg\" in --resume|resume) echo '" + resumeOutput + "'; exit " +
		strconv.Itoa(resumeCode) + ";; esac\ndone\necho started\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, log
}

func fakeAgentInvocations(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var invocations []string
	for _, record := range strings.Split(string(data), "<<<END>>>\n") {
		if record = strings.TrimSpace(record); record != "" {
			invocations = append(invocations, record)
		}
	}
	return invocations
}

func TestCommandRunnerRestartsClaudeWhenResumedSessionIsMissing(t *testing.T) {
	binary, log := writeFakeAgent(t, `{"type":"result","is_error":true,"result":"No conversation found with session ID: session-7"}`, 1)
	runner := CommandRunner{Agent: "claude", ClaudeBinary: binary, Repo: "o/r"}
	session := AgentSession{ID: "session-7", Agent: "claude", Resume: true}
	if err := runner.RunSession(context.Background(), Issue{Number: 7}, session, func(AgentSession) {}); err != nil {
		t.Fatalf("missing session should restart the work, got %v", err)
	}
	got := fakeAgentInvocations(t, log)
	if len(got) != 2 {
		t.Fatalf("agent invocations = %#v, want a resume followed by a fresh run", got)
	}
	if !strings.Contains(got[0], "--resume session-7") {
		t.Fatalf("first invocation = %q, want a resume", got[0])
	}
	if strings.Contains(got[1], "--resume") || !strings.Contains(got[1], "/gh-fix 7") {
		t.Fatalf("second invocation = %q, want a fresh run", got[1])
	}
	// Claude accepts the caller's session ID, so the restarted run keeps the
	// identity already persisted in the work state.
	if !strings.Contains(got[1], "--session-id session-7") {
		t.Fatalf("second invocation = %q, want the persisted session ID reused", got[1])
	}
}

func TestCommandRunnerRestartsCodexWithoutTheMissingSessionID(t *testing.T) {
	binary, log := writeFakeAgent(t, "Error: session not found", 1)
	runner := CommandRunner{Agent: "codex", CodexBinary: binary, Repo: "o/r"}
	session := AgentSession{ID: "session-7", Agent: "codex", Resume: true}
	if err := runner.RunSession(context.Background(), Issue{Number: 7}, session, func(AgentSession) {}); err != nil {
		t.Fatalf("missing session should restart the work, got %v", err)
	}
	got := fakeAgentInvocations(t, log)
	if len(got) != 2 {
		t.Fatalf("agent invocations = %#v, want a resume followed by a fresh run", got)
	}
	if strings.Contains(got[1], "session-7") || !strings.Contains(got[1], "/gh-fix 7") {
		t.Fatalf("second invocation = %q, want a fresh run without the dead session ID", got[1])
	}
}

func TestCommandRunnerReportsResumeFailuresThatAreNotMissingSessions(t *testing.T) {
	binary, log := writeFakeAgent(t, "boom: the agent crashed", 3)
	runner := CommandRunner{Agent: "claude", ClaudeBinary: binary, Repo: "o/r"}
	session := AgentSession{ID: "session-7", Agent: "claude", Resume: true}
	err := runner.RunSession(context.Background(), Issue{Number: 7}, session, func(AgentSession) {})
	if err == nil || !strings.Contains(err.Error(), "bug report") {
		t.Fatalf("unrelated resume failure = %v, want a reported agent failure", err)
	}
	if got := fakeAgentInvocations(t, log); len(got) != 1 {
		t.Fatalf("agent invocations = %#v, want no restart", got)
	}
}

func TestMissingSessionDetectorMatchesAcrossWrites(t *testing.T) {
	detector := &missingSessionDetector{output: io.Discard}
	for _, chunk := range []string{"No conversation ", "found with session ID: x\n"} {
		if _, err := detector.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if !detector.sessionMissing() {
		t.Fatal("split missing-session message was not detected")
	}
	other := &missingSessionDetector{output: io.Discard}
	if _, err := other.Write([]byte("compilation failed")); err != nil {
		t.Fatal(err)
	}
	if other.sessionMissing() {
		t.Fatal("unrelated output must not be treated as a missing session")
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

func TestCommandRunnerAgentNameLoadBalancesEvenlyAcrossAgents(t *testing.T) {
	runner := CommandRunner{Agents: []string{"codex", "claude"}, agentCursor: &atomic.Uint64{}}
	got := make([]string, 6)
	for i := range got {
		got[i] = runner.AgentName()
	}
	want := []string{"codex", "claude", "codex", "claude", "codex", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent rotation = %#v, want %#v", got, want)
	}
}

func TestCommandRunnerAgentNameWithoutAgentsFallsBackToAgent(t *testing.T) {
	if got, want := (CommandRunner{Agent: "claude"}).AgentName(), "claude"; got != want {
		t.Fatalf("AgentName() = %q, want %q", got, want)
	}
	if got, want := (CommandRunner{}).AgentName(), "codex"; got != want {
		t.Fatalf("AgentName() = %q, want %q", got, want)
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

func TestSessionMetadataCaptureWriterCapturesCheckoutWrappedInMarkdown(t *testing.T) {
	checkout := t.TempDir()
	var updates []AgentSession
	w := &sessionMetadataCaptureWriter{
		output:   io.Discard,
		onUpdate: func(update AgentSession) { updates = append(updates, update) },
	}
	_, _ = io.WriteString(w, "`GLORP_CHECKOUT_DIRECTORY="+checkout+"`\n")
	want := []AgentSession{{CheckoutDirectory: checkout}}
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

func TestClaudeJSONOutputWriterDecodesStreamEvents(t *testing.T) {
	var output bytes.Buffer
	w := newClaudeJSONOutputWriter(&output)
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking into the issue."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
	}
	for _, line := range lines {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	want := "Looking into the issue.\nRunning: go test ./...\nRunning: Read main.go\n"
	if got := output.String(); got != want {
		t.Fatalf("decoded output = %q, want %q", got, want)
	}
}

func TestClaudeToolUseSummaryAddsContext(t *testing.T) {
	longPath := "/tmp/" + strings.Repeat("nested/", 30) + "deep.go"
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{name: "bash command drops tool name", tool: "Bash", input: `{"command":"go test ./..."}`, want: "go test ./..."},
		{name: "read shows file", tool: "Read", input: `{"file_path":"main.go","limit":20}`, want: "Read main.go"},
		{name: "edit shows file not contents", tool: "Edit", input: `{"file_path":"ui.go","old_string":"a","new_string":"b"}`, want: "Edit ui.go"},
		{name: "notebook shows path", tool: "NotebookEdit", input: `{"notebook_path":"run.ipynb"}`, want: "NotebookEdit run.ipynb"},
		{name: "grep shows pattern", tool: "Grep", input: `{"pattern":"Running:","path":"web/src"}`, want: "Grep Running:"},
		{name: "glob shows pattern", tool: "Glob", input: `{"pattern":"**/*.go"}`, want: "Glob **/*.go"},
		{name: "fetch shows url", tool: "WebFetch", input: `{"url":"https://example.com/a","prompt":"summarize"}`, want: "WebFetch https://example.com/a"},
		{name: "search shows query", tool: "WebSearch", input: `{"query":"go stream json"}`, want: "WebSearch go stream json"},
		{name: "task shows description", tool: "Task", input: `{"description":"audit webhooks","subagent_type":"Explore"}`, want: "Task audit webhooks"},
		{name: "list shows path", tool: "Bash", input: `{"path":"/tmp"}`, want: "Bash /tmp"},
		{name: "detail collapses whitespace", tool: "Task", input: `{"description":"audit\n  webhooks"}`, want: "Task audit webhooks"},
		{name: "no useful field keeps name", tool: "TodoWrite", input: `{"todos":[{"content":"x"}]}`, want: "TodoWrite"},
		{name: "empty field keeps name", tool: "Read", input: `{"file_path":"  "}`, want: "Read"},
		{name: "missing input keeps name", tool: "TodoWrite", input: ``, want: "TodoWrite"},
		{name: "invalid input keeps name", tool: "Read", input: `not json`, want: "Read"},
		{
			name: "long prompt truncates from the end", tool: "Task",
			input: `{"description":"` + strings.Repeat("x", 200) + `"}`,
			want:  "Task " + strings.Repeat("x", 120) + "…",
		},
		{
			name: "long path truncates from the front", tool: "Read",
			input: `{"file_path":"` + longPath + `"}`,
			want:  "Read …" + longPath[len(longPath)-120:],
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeToolUseSummary(test.tool, json.RawMessage(test.input)); got != test.want {
				t.Fatalf("claudeToolUseSummary(%q, %s) = %q, want %q", test.tool, test.input, got, test.want)
			}
		})
	}
}

func TestClaudeJSONOutputWriterSurfacesErrorResults(t *testing.T) {
	var output bytes.Buffer
	w := newClaudeJSONOutputWriter(&output)
	if _, err := io.WriteString(w, `{"type":"result","is_error":true,"result":"context deadline exceeded"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	want := "Agent error: context deadline exceeded\n"
	if got := output.String(); got != want {
		t.Fatalf("decoded output = %q, want %q", got, want)
	}
}

func TestClaudeJSONOutputWriterPassesThroughNonJSONLines(t *testing.T) {
	var output bytes.Buffer
	w := newClaudeJSONOutputWriter(&output)
	if _, err := io.WriteString(w, "not json\n"); err != nil {
		t.Fatal(err)
	}
	want := "not json\n"
	if got := output.String(); got != want {
		t.Fatalf("decoded output = %q, want %q", got, want)
	}
}

func TestClaudeJSONOutputWriterFlushesTrailingPartialLine(t *testing.T) {
	var output bytes.Buffer
	w := newClaudeJSONOutputWriter(&output)
	if _, err := io.WriteString(w, `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}`); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "" {
		t.Fatalf("output before flush = %q, want empty", got)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "partial\n"; got != want {
		t.Fatalf("output after flush = %q, want %q", got, want)
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
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7}}}}, Runner: runner,
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

func TestGlorpReclaimsFailedPersistedSession(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	want := workState{Status: "failed", SessionID: "session-167", Agent: "codex", CheckoutDirectory: dir}
	if err := saveWorkState(statePath, map[int]workState{167: want}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeSessionRunner{agent: "claude", sessions: make(chan AgentSession, 1)}
	w := &Glorp{
		Repo: "lsegal/glorp", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 167}}}}, Runner: runner,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case got := <-runner.sessions:
		if !got.Resume || got.ID != want.SessionID || got.Agent != want.Agent || got.CheckoutDirectory != want.CheckoutDirectory {
			t.Fatalf("reclaimed session = %#v, want persisted %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("failed persisted session was not reclaimed")
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

func TestWebhookEventNeedsRefresh(t *testing.T) {
	tests := []struct {
		event WebhookEvent
		want  bool
	}{
		{event: WebhookEvent{Kind: "push", Ref: "refs/heads/main", CommitCount: 3}},
		{event: WebhookEvent{Kind: "ping"}},
		{event: WebhookEvent{Kind: "pull_request", Action: "opened"}},
		{event: WebhookEvent{Kind: "pull_request", Action: "closed"}},
		{event: WebhookEvent{Kind: "issue_comment", Action: "created"}},
		{event: WebhookEvent{Kind: "issues", Action: "edited"}},
		{event: WebhookEvent{Kind: "issues", Action: "assigned"}},
		{event: WebhookEvent{Kind: "issues", Action: "locked"}},
		{event: WebhookEvent{Kind: "issues", Action: "opened"}, want: true},
		{event: WebhookEvent{Kind: "issues", Action: "reopened"}, want: true},
		{event: WebhookEvent{Kind: "issues", Action: "closed"}, want: true},
		{event: WebhookEvent{Kind: "issues", Action: "labeled"}, want: true},
		{event: WebhookEvent{Kind: "issues", Action: "unlabeled"}, want: true},
		{event: WebhookEvent{Kind: "issues", Action: "transferred"}, want: true},
		{event: WebhookEvent{Kind: "projects_v2_item", Action: "reordered"}},
		{event: WebhookEvent{Kind: "projects_v2_item", Action: "edited"}, want: true},
		{event: WebhookEvent{Kind: "projects_v2_item", Action: "created"}, want: true},
		{event: WebhookEvent{Kind: "discussion", Action: "edited"}},
		{event: WebhookEvent{Kind: "discussion", Action: "answered"}},
		{event: WebhookEvent{Kind: "discussion", Action: "labeled"}},
		{event: WebhookEvent{Kind: "discussion", Action: "created"}, want: true},
		{event: WebhookEvent{Kind: "discussion", Action: "reopened"}, want: true},
		{event: WebhookEvent{Kind: "discussion", Action: "transferred"}, want: true},
		{event: WebhookEvent{Kind: "release", Action: "published"}, want: true},
	}
	for _, test := range tests {
		t.Run(webhookEventLabel(test.event), func(t *testing.T) {
			if got := webhookEventNeedsRefresh(test.event); got != test.want {
				t.Fatalf("needs refresh = %t, want %t", got, test.want)
			}
		})
	}
}

func TestGlorpSkipsRefreshForIrrelevantWebhookDeliveries(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{}}}
	r := &fakeRunner{release: make(chan struct{})}
	events := make(chan WebhookEvent, 4)
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: r,
		UseWebhooks: true, Events: events, fallbackInterval: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	waitForCalls := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			src.mu.Lock()
			calls := src.calls
			src.mu.Unlock()
			if calls >= want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d list call(s)", want)
	}
	waitForCalls(1)

	events <- WebhookEvent{Kind: "push", Repository: "o/r", Ref: "refs/heads/main", CommitCount: 2}
	events <- WebhookEvent{Kind: "pull_request", Repository: "o/r", Action: "synchronize"}
	events <- WebhookEvent{Kind: "issues", Repository: "o/r", Action: "edited", IssueNumber: 7}
	// A relevant delivery queued last drains the ignored ones ahead of it, so
	// its own refresh proves they were processed without polling.
	events <- WebhookEvent{Kind: "issues", Repository: "o/r", Action: "opened", IssueNumber: 8}
	waitForCalls(2)

	src.mu.Lock()
	calls := src.calls
	src.mu.Unlock()
	if calls != 2 {
		t.Fatalf("list calls = %d, want 2 (initial poll plus the relevant delivery)", calls)
	}
	close(r.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpStopsWebhookFollowUpOnceIssueIsObserved(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{}, {{Number: 1, Repository: "o/r"}}}}
	r := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: 20 * time.Millisecond, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: r,
		UseWebhooks: true, Events: events, fallbackInterval: time.Hour,
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
	events <- WebhookEvent{Kind: "issues", Action: "opened", Repository: "o/r", IssueNumber: 1}

	select {
	case n := <-r.dispatched:
		if n != 1 {
			t.Fatalf("dispatched issue #%d, want #1", n)
		}
	case <-time.After(time.Second):
		t.Fatal("issue #1 was not dispatched")
	}

	// The webhook-triggered refresh already observed issue #1, so the full
	// follow-up chain (three more polls at the 20ms interval) must not run.
	time.Sleep(150 * time.Millisecond)
	src.mu.Lock()
	calls := src.calls
	src.mu.Unlock()
	if calls != 2 {
		t.Fatalf("list calls = %d, want 2 (initial poll plus the webhook refresh)", calls)
	}
	close(r.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
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

func TestShouldDispatchIssueUsesProjectStatusForRecovery(t *testing.T) {
	project := "https://github.com/users/lsegal/projects/3"
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "In Progress"}, false, false, false, false, "") {
		t.Fatal("in-progress project item stranded by another instance was not a reclaim candidate")
	}
	for _, status := range []string{"Done", "Completed"} {
		if shouldDispatchIssue(project, Issue{ProjectStatus: status}, false, false, false, false, "") {
			t.Fatalf("new %s project issue was dispatched", status)
		}
	}
	for _, status := range []string{"Todo", "TODO", "Ready", "ready"} {
		if !shouldDispatchIssue(project, Issue{ProjectStatus: status}, false, false, false, false, "") {
			t.Fatalf("new %s project issue was not dispatched", status)
		}
	}
	if shouldDispatchIssue(project, Issue{ProjectStatus: "Backlog"}, false, false, false, false, "") {
		t.Fatal("new backlog project issue was dispatched")
	}
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "Agent Queue"}, false, false, false, false, "agent queue") {
		t.Fatal("configured ready project issue was not dispatched")
	}
	if shouldDispatchIssue(project, Issue{ProjectStatus: "Ready"}, false, false, false, false, "Agent Queue") {
		t.Fatal("default ready status was used despite configured ready state")
	}
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "In Progress"}, false, false, false, true, "") {
		t.Fatal("in-progress project issue was not reclaimed")
	}
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "in progress"}, false, false, false, true, "") {
		t.Fatal("case-insensitive in-progress project issue was not reclaimed")
	}
	if !shouldDispatchIssue(project, Issue{ProjectStatus: "Todo"}, false, false, false, true, "") {
		t.Fatal("previously seen project issue moved back to Todo was not dispatched")
	}
	if shouldDispatchIssue(project, Issue{ProjectStatus: "Backlog"}, false, false, false, true, "") {
		t.Fatal("previously seen backlog project issue was dispatched")
	}
	if !shouldDispatchIssue("o/r", Issue{}, false, false, false, true, "") {
		t.Fatal("previously seen repository issue was not made a reclaim candidate")
	}
	if shouldDispatchIssue("o/r", Issue{}, false, false, true, true, "") {
		t.Fatal("previously completed repository issue was redispatched")
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
		{name: "repository active session is resumable", target: "o/r", state: workState{Status: "active", SessionID: "s", Agent: "codex"}, want: true},
		{name: "repository active session missing agent is stale", target: "o/r", state: workState{Status: "active", SessionID: "s"}},
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

func TestGlorpRedispatchesProjectItemMovedBackToTodoAfterCompletion(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{batches: [][]Issue{{{Number: 7, ProjectStatus: "Todo"}}}}
	// allow gates each dispatch individually so a redispatch that races ahead
	// of the assertion can't complete on its own and trigger further
	// redispatches before the test observes it; dispatched reports each
	// dispatch as it happens instead of requiring the test to poll r.got
	// against a wall-clock deadline.
	r := &fakeRunner{release: make(chan struct{}), allow: make(chan struct{}), dispatched: make(chan int, 1)}
	var logs bytes.Buffer
	w := &Glorp{
		Repo: "https://github.com/users/o/projects/3", Interval: time.Millisecond, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: r, Out: &logs,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	const waitTimeout = 10 * time.Second
	awaitDispatch := func(label string) {
		t.Helper()
		select {
		case got := <-r.dispatched:
			if got != 7 {
				t.Fatalf("%s: dispatched issue #%d, want #7", label, got)
			}
		case <-time.After(waitTimeout):
			cancel()
			<-done
			t.Fatalf("timed out waiting for %s", label)
		}
	}

	awaitDispatch("initial dispatch")
	// Let the first run complete so the item's local state becomes
	// "completed" while the project item remains in the Todo column.
	r.allow <- struct{}{}
	awaitDispatch("redispatch after completion")

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if want := "reset stale local completed state"; !strings.Contains(logs.String(), want) {
		t.Fatalf("logs did not report completed reset:\n%s", logs.String())
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

// fakeProjectState serves board fingerprints for the push-mode project probe.
type fakeProjectState struct {
	mu     sync.Mutex
	calls  int
	states []string
	err    error
}

func (f *fakeProjectState) ProjectState(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(f.states) == 0 {
		return "", nil
	}
	if f.calls > len(f.states) {
		return f.states[len(f.states)-1], nil
	}
	return f.states[f.calls-1], nil
}

func (f *fakeProjectState) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestGlorpDispatchesProjectBoardChangeBeforeFallbackPoll(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{
		{}, // initial poll: nothing on the board yet
		{{Number: 9, ProjectStatus: "Todo", State: "OPEN"}}, // card moved into Todo
	}}
	r := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	// The board changes on the second probe, and the fallback poll is an hour
	// away, so only the probe can produce this dispatch.
	board := &fakeProjectState{states: []string{"before", "after"}}
	w := &Glorp{
		Repo: "https://github.com/users/o/projects/3", Interval: time.Millisecond, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: r, UseWebhooks: true,
		Projects: board, fallbackInterval: time.Hour, probeInterval: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case number := <-r.dispatched:
		if number != 9 {
			t.Fatalf("dispatched issue #%d, want #9", number)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("board-only project change was not dispatched before the fallback poll")
	}
	close(r.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpSkipsPollWhileProjectBoardIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{}}}
	board := &fakeProjectState{states: []string{"steady"}}
	w := &Glorp{
		Repo: "https://github.com/users/o/projects/3", Interval: time.Millisecond, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: &fakeRunner{release: make(chan struct{})},
		UseWebhooks: true, Projects: board, fallbackInterval: time.Hour, probeInterval: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && board.callCount() < 10 {
		time.Sleep(time.Millisecond)
	}
	probes := board.callCount()
	src.mu.Lock()
	calls := src.calls
	src.mu.Unlock()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if probes < 10 {
		t.Fatalf("board probed %d time(s), want at least 10", probes)
	}
	if calls != 1 {
		t.Fatalf("idle board triggered %d issue list call(s), want only the initial poll", calls)
	}
}

func TestGlorpDoesNotProbeBoardsWithoutProjectTargetsOrPushMode(t *testing.T) {
	for _, test := range []struct {
		name        string
		targets     []string
		useWebhooks bool
		projects    ProjectStateSource
		want        int
	}{
		{name: "push project target", targets: []string{"https://github.com/users/o/projects/3"}, useWebhooks: true, projects: &fakeProjectState{}, want: 1},
		{name: "repository target", targets: []string{"o/r"}, useWebhooks: true, projects: &fakeProjectState{}, want: 0},
		{name: "poll mode", targets: []string{"https://github.com/users/o/projects/3"}, useWebhooks: false, projects: &fakeProjectState{}, want: 0},
		{name: "no source", targets: []string{"https://github.com/users/o/projects/3"}, useWebhooks: true, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := &Glorp{UseWebhooks: test.useWebhooks, Projects: test.projects}
			if got := len(w.projectProbeTargets(test.targets)); got != test.want {
				t.Fatalf("probed targets = %d, want %d", got, test.want)
			}
		})
	}
}

func TestProjectBoardProbeInterval(t *testing.T) {
	if got := (&Glorp{}).projectBoardProbeInterval(); got != projectProbeInterval {
		t.Fatalf("default probe interval = %s, want %s", got, projectProbeInterval)
	}
	if got := (&Glorp{probeInterval: time.Second}).projectBoardProbeInterval(); got != time.Second {
		t.Fatalf("override probe interval = %s, want 1s", got)
	}
	if projectProbeInterval >= pushFallbackInterval {
		t.Fatalf("probe interval %s must be shorter than the fallback interval %s", projectProbeInterval, pushFallbackInterval)
	}
}

// Push webhooks have to be reconciled while the daemon runs, not only at
// startup, so a repository added to a project board later gets a webhook
// without a restart (issue #238).
func TestGlorpReconcilesWebhooksOnPeriodicPoll(t *testing.T) {
	src := &fakeSource{batches: [][]Issue{{}}}
	reconciled := make(chan struct{}, 1)
	w := &Glorp{
		Repo: "https://github.com/users/o/projects/3", Interval: time.Millisecond, Concurrency: 1,
		Issues: src, Runner: &fakeRunner{release: make(chan struct{})}, UseWebhooks: true,
		fallbackInterval: time.Millisecond,
		Webhooks: func(context.Context) {
			select {
			case reconciled <- struct{}{}:
			default:
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("periodic poll did not reconcile webhooks")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpReapsOnItsOwnTickerWhenPollingIsSlow(t *testing.T) {
	src := &fakeSource{batches: [][]Issue{{}}}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: src, Runner: &fakeRunner{release: make(chan struct{})}, UseWebhooks: true,
		fallbackInterval: time.Hour, reapInterval: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 3 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("reap ticker did not run additional polls while the poll interval was an hour away")
}
