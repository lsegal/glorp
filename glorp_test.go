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
func TestGlorpRunsUnseenIssuesWithLimit(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 1}, {Number: 2}}, {{Number: 1}, {Number: 2}, {Number: 3}, {Number: 4}}}}
	r := &fakeRunner{release: make(chan struct{})}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Glorp{Repo: "o/r", Interval: time.Millisecond, Concurrency: 2, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: logs}
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
	r := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Glorp{Repo: target, Interval: time.Millisecond, Concurrency: 2, StatePath: filepath.Join(dir, "state"), Issues: src, Discussions: ds, Runner: r, Out: logs}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case number := <-r.dispatched:
		if number != 9 {
			t.Fatalf("dispatched discussion #%d, want #9", number)
		}
	case <-time.After(time.Second):
		t.Fatal("discussion was not dispatched")
	}
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

func TestGlorpDispatchesDirectIdentityMention(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}, {{Number: 7}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 2)}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, UseWebhooks: true, Events: events,
		fallbackInterval: time.Hour, Identity: "SELF",
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-runner.dispatched:
	case <-time.After(time.Second):
		t.Fatal("initial issue was not dispatched")
	}
	close(runner.release)

	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 7, CommentBody: "Please revisit this @/glorp:SELF"}
	select {
	case n := <-runner.dispatched:
		if n != 7 {
			t.Fatalf("direct mention dispatched issue #%d, want #7", n)
		}
	case <-time.After(time.Second):
		t.Fatal("direct identity mention did not redispatch issue")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpRetriesPendingDirectMentionAfterIssueListLag(t *testing.T) {
	dir := t.TempDir()
	// The immediate direct-mention refresh can race GitHub's issue search
	// index. It must retry until it sees and dispatches the mentioned issue,
	// rather than treating an empty list as a completed retry.
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}, {}, {{Number: 7}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 2)}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, UseWebhooks: true, Events: events,
		fallbackInterval: time.Hour, Identity: "SELF",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-runner.dispatched:
	case <-time.After(time.Second):
		t.Fatal("initial issue was not dispatched")
	}
	close(runner.release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err := loadWorkState(w.StatePath)
		if err == nil && state[7].Status == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 7, CommentBody: "Please revisit this @/glorp:SELF"}
	select {
	case n := <-runner.dispatched:
		if n != 7 {
			t.Fatalf("direct mention dispatched issue #%d, want #7", n)
		}
	case <-time.After(time.Second):
		t.Fatal("pending direct mention was not retried after an empty issue list")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpIgnoresDirectMentionFromDisallowedCommenter(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}, {{Number: 7}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 2)}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, UseWebhooks: true, Events: events,
		fallbackInterval: time.Hour, Identity: "SELF", AllowedCommenters: []string{"trusted"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-runner.dispatched:
	case <-time.After(time.Second):
		t.Fatal("initial issue was not dispatched")
	}
	close(runner.release)

	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 7, CommentBody: "Please revisit this @/glorp:SELF", CommentAuthor: "impersonator"}
	select {
	case n := <-runner.dispatched:
		t.Fatalf("mention from a disallowed commenter should not redispatch issue, got #%d", n)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpIgnoresDirectMentionThatIsNotTheLastComment(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7}}, {{Number: 7}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 2)}
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
	case <-runner.dispatched:
	case <-time.After(time.Second):
		t.Fatal("initial issue was not dispatched")
	}
	close(runner.release)

	// A follow-up comment posted after the mention (but before glorp reacts to
	// the webhook delivery) means the mention is no longer the last comment.
	comments.inject("o/r", 7, Comment{Body: "Please revisit this @/glorp:SELF", Author: "lsegal", CreatedAt: time.Now()})
	comments.inject("o/r", 7, Comment{Body: "actually never mind", Author: "lsegal", CreatedAt: time.Now().Add(time.Second)})

	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 7, CommentBody: "Please revisit this @/glorp:SELF", CommentAuthor: "lsegal"}
	select {
	case n := <-runner.dispatched:
		t.Fatalf("a stale mention buried under a newer comment should not redispatch issue, got #%d", n)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMentionedIdentityOnlyMatchesAddressedInstance(t *testing.T) {
	if !mentionedIdentity("Please retry @/glorp:SELF.", "SELF") {
		t.Fatal("expected direct mention to match")
	}
	for _, body := range []string{"Please retry @/glorp:OTHER", "Please retry @/glorp:SELFISH", "handoff /glorp:SELF"} {
		if mentionedIdentity(body, "SELF") {
			t.Fatalf("unexpected identity match for %q", body)
		}
	}
}

func TestGlorpStopsAgentWhenOriginatingWorkCloses(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeClosureSource{fakeSource: &fakeSource{batches: [][]Issue{{{Number: 7}}}}}
	runner := &fakeRunner{release: make(chan struct{})}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
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

func TestGlorpWebUIRetriesActiveWork(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	src := &fakeSource{batches: [][]Issue{{{Number: 7, Title: "Retry me"}}}}
	runner := &fakeSessionRunner{agent: "codex", sessions: make(chan AgentSession, 2), reported: AgentSession{ID: "session-7"}}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	<-runner.sessions
	if err := w.handleJobAction(ctx, jobAction{Action: "retry", Target: "o/r", Number: 7}); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	select {
	case session := <-runner.sessions:
		if !session.Resume {
			t.Fatal("retry did not rerun the existing gh-fix session")
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("retry did not rerun gh-fix")
	}
	if !strings.Contains(logs.String(), "retry requested from web UI; stopping and rerunning gh-fix") {
		t.Fatalf("action intent was not logged:\n%s", logs.String())
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
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
func TestGlorpIncludesAgentSpecInJobSnapshot(t *testing.T) {
	reporter := &snapshotReporter{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 1, Title: "bug"}}}},
		Runner: &fakeSessionRunner{agent: "claude/opus:low", sessions: make(chan AgentSession, 1)}, UI: reporter,
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
			if job.Number == 1 && job.Agent != "" {
				if job.Agent != "claude" || job.Model != "opus" || job.Effort != "low" {
					t.Fatalf("job agent spec was not parsed into the snapshot: %+v", job)
				}
				return
			}
		}
	}
	t.Fatalf("agent spec was not included in any job snapshot: %+v", reporter.snapshots)
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 2,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7}, {Number: 8}}}}, Runner: r, Status: status, Out: logs,
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
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: 10 * time.Millisecond, Concurrency: 1, Issues: src, Runner: r, Out: logs}
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
	if !strings.Contains(logs.String(), "initial poll #1 error") {
		t.Fatalf("logs = %q, want an initial poll error message", logs.String())
	}
}

// TestPollLoggingIsQuietWhileNothingChanges covers issue #413: a five-second
// poll loop wrote a start line, a count, and a completion line on every tick,
// so a repository with no ready work filled the log with the same three lines
// forever. Only a change in what the poll found is worth a line.
func TestPollLoggingIsQuietWhileNothingChanges(t *testing.T) {
	src := &fakeSource{batches: [][]Issue{{}}}
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, Issues: src, Runner: &fakeRunner{release: make(chan struct{})}, Out: logs}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 5 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("only %d poll(s) ran", calls)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if got := strings.Count(out, "found 0 open issue(s)"); got != 1 {
		t.Fatalf("logged the same empty result %d times, want once: %q", got, out)
	}
	for _, unwanted := range []string{"poll #1 started", "no new issues"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("logs still contain %q on an unchanged poll: %q", unwanted, out)
		}
	}
}

// TestRepeatedPollFailureIsLoggedOnce covers the other half of issue #413: a
// failing poll logged the failure where it was raised and again where it was
// returned, on every tick, so one broken listing produced two lines every five
// seconds. The failure is now reported once, by the caller, and the recovery
// says the reported failure is over.
func TestRepeatedPollFailureIsLoggedOnce(t *testing.T) {
	src := &erroringThenSucceedingSource{failN: 3, batches: [][]Issue{{}}}
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, Issues: src, Runner: &fakeRunner{release: make(chan struct{})}, Out: logs}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "the failure reported above is resolved") {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("the poll never recovered: %q", logs.String())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if got := strings.Count(out, "poll #1 error"); got != 1 {
		t.Fatalf("logged the initial failure %d times, want once: %q", got, out)
	}
	if got := strings.Count(out, " error: "); got != 1 {
		t.Fatalf("logged %d failure lines for one repeating failure, want one: %q", got, out)
	}
	if strings.Contains(out, "failed while listing") {
		t.Fatalf("the listing failure is still logged where it is raised as well: %q", out)
	}
	if !strings.Contains(out, "listing o/r: transient listing failure") {
		t.Fatalf("the reported failure lost the target it was raised for: %q", out)
	}
}

func TestInvalidRepo(t *testing.T) {
	w := &Glorp{Repo: "bad", Interval: time.Second, Concurrency: 1}
	if w.Run(context.Background()) == nil {
		t.Fatal("expected error")
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: runner, Out: logs,
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: runner, Out: logs,
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
	prompt := "/gh-fix owner/repo#12 identity:/glorp:ABC\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	issue := Issue{Number: 12, Repository: "owner/repo", Target: "https://github.com/users/owner/projects/3"}
	got := commandArgs(CommandRunner{Agent: "codex", Repo: "wrong/repo", Identity: "ABC"}, issue)
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

// writeFakeAgent turns the test binary itself into an executable stub that
// appends each invocation's arguments to a log file and emits the supplied
// lines, exiting with code when the invocation is a resume. Re-executing the
// test binary keeps the stub portable, where a shell script would not run on
// Windows; see TestMain for the dispatch.
func writeFakeAgent(t *testing.T, resumeOutput string, resumeCode int) (binary, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "invocations.log")
	t.Setenv(fakeAgentLogEnv, log)
	t.Setenv(fakeAgentResumeOutputEnv, resumeOutput)
	t.Setenv(fakeAgentResumeCodeEnv, strconv.Itoa(resumeCode))
	return os.Args[0], log
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
	if strings.Contains(got[1], "--resume") || !strings.Contains(got[1], "/gh-fix o/r#7") {
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
	if strings.Contains(got[1], "session-7") || !strings.Contains(got[1], "/gh-fix o/r#7") {
		t.Fatalf("second invocation = %q, want a fresh run without the dead session ID", got[1])
	}
}

// TestCommandRunnerRunsOutsideAmbientWorkingDirectory guards against issue
// #279: a fresh (non-resumed) run must not inherit glorp's own working
// directory, since that directory can belong to an unrelated git repository
// (e.g. the repo `glorp watch` happened to be launched from) and mislead the
// agent's own ambient-repo detection into targeting the wrong repository.
func TestCommandRunnerRunsOutsideAmbientWorkingDirectory(t *testing.T) {
	binary, log := writeFakeAgent(t, "", 0)
	runner := CommandRunner{Agent: "codex", CodexBinary: binary, Repo: "o/r"}
	session := AgentSession{ID: "session-7", Agent: "codex"}
	if err := runner.RunSession(context.Background(), Issue{Number: 7, Target: "o/r"}, session, func(AgentSession) {}); err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	got := fakeAgentInvocations(t, log)
	if len(got) != 1 {
		t.Fatalf("agent invocations = %#v, want exactly one", got)
	}
	ambientWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got[0], "cwd="+ambientWD) {
		t.Fatalf("agent invocation ran in glorp's own working directory: %q", got[0])
	}
}

// TestCommandRunnerDisablesClaudeBackgroundWaitCeiling guards against issue
// #330: Claude Code's headless print mode terminates in-flight background
// shell tasks after a 10 minute ceiling, making long-lived autonomous runs
// appear to give up mid-task. glorp must disable that ceiling for claude.
func TestCommandRunnerDisablesClaudeBackgroundWaitCeiling(t *testing.T) {
	binary, log := writeFakeAgent(t, "", 0)
	runner := CommandRunner{Agent: "claude", ClaudeBinary: binary, Repo: "o/r"}
	session := AgentSession{ID: "session-7", Agent: "claude"}
	if err := runner.RunSession(context.Background(), Issue{Number: 7}, session, func(AgentSession) {}); err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	got := fakeAgentInvocations(t, log)
	if len(got) != 1 || !strings.Contains(got[0], "bg_wait_ceiling_set=true bg_wait_ceiling=0") {
		t.Fatalf("agent invocations = %#v, want CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0", got)
	}
}

func TestCommandRunnerLeavesCodexBackgroundWaitCeilingUnset(t *testing.T) {
	if previous, ok := os.LookupEnv("CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS"); ok {
		os.Unsetenv("CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS")
		t.Cleanup(func() { os.Setenv("CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS", previous) })
	}
	binary, log := writeFakeAgent(t, "", 0)
	runner := CommandRunner{Agent: "codex", CodexBinary: binary, Repo: "o/r"}
	session := AgentSession{ID: "session-7", Agent: "codex"}
	if err := runner.RunSession(context.Background(), Issue{Number: 7}, session, func(AgentSession) {}); err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	got := fakeAgentInvocations(t, log)
	if len(got) != 1 || !strings.Contains(got[0], "bg_wait_ceiling_set=false") {
		t.Fatalf("agent invocations = %#v, want the ceiling env var left unset for codex", got)
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

func TestScopedWorkStateIgnoresUnwatchedTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := map[string]workState{
		"o/current#7": {Status: "completed", SessionID: "current"},
		"o/old#9":     {Status: "failed", SessionID: "old"},
	}
	if err := saveScopedWorkState(path, state, []string{"o/current", "o/old"}); err != nil {
		t.Fatal(err)
	}
	got, err := loadScopedWorkState(path, []string{"o/current"})
	want := map[string]workState{
		"o/current#7": {Status: "completed", SessionID: "current"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped state error=%v value=%v, want %v", err, got, want)
	}
	if err := saveScopedWorkState(path, got, []string{"o/current"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadWorkState(path)
	if err != nil || legacy[7].SessionID != "current" || len(legacy) != 1 {
		t.Fatalf("legacy state error=%v value=%v", err, legacy)
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
		{event: WebhookEvent{Kind: "pull_request", Action: "closed"}, want: true},
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

func TestGlorpResumesFailedReferencedWorkWhenPullRequestCloses(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	want := workState{Status: "failed", SessionID: "session-7", Agent: "codex", CheckoutDirectory: dir}
	if err := saveWorkState(statePath, map[int]workState{7: want}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{batches: [][]Issue{
		{{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "open"}}}},
		{{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "closed"}}}},
	}}
	runner := &fakeSessionRunner{agent: "claude", sessions: make(chan AgentSession, 1)}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, UseWebhooks: true, Events: events, fallbackInterval: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	for {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	events <- WebhookEvent{Kind: "pull_request", Action: "closed", Repository: "o/r", MentionedIssues: []int{7}}

	select {
	case got := <-runner.sessions:
		if !got.Resume || got.ID != want.SessionID || got.Agent != want.Agent || got.CheckoutDirectory != want.CheckoutDirectory {
			t.Fatalf("resumed session = %#v, want persisted %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("closed pull request did not immediately resume referenced work")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpNegotiatesReferencedWorkWhenPullRequestCloses(t *testing.T) {
	dir := t.TempDir()
	source := &fakeClosureSource{
		fakeSource: &fakeSource{batches: [][]Issue{
			{{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "open"}}}},
			{{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "open"}}}},
			{{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "closed"}}}},
		}},
		state: OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 8, State: "open"}}},
	}
	comments := newFakeCommentClient()
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	events := make(chan WebhookEvent, 1)
	w := &Glorp{
		Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: source, Runner: runner, UseWebhooks: true, Events: events, fallbackInterval: time.Hour,
		Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool { return true },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	for {
		source.fakeSource.mu.Lock()
		calls := source.fakeSource.calls
		source.fakeSource.mu.Unlock()
		if calls >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	events <- WebhookEvent{Kind: "pull_request", Action: "closed", Repository: "o/r", MentionedIssues: []int{7}}

	select {
	case got := <-runner.dispatched:
		if got != 7 {
			t.Fatalf("dispatched issue #%d, want #7", got)
		}
	case <-time.After(time.Second):
		t.Fatal("closed pull request did not dispatch referenced work after handoff")
	}
	posted, err := comments.ListComments(context.Background(), "o/r", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) != 2 {
		t.Fatalf("pull request comments = %v, want an ask followed by a continuing claim", posted)
	}
	if kind, id, ok := parseClaim(posted[0].Body); !ok || kind != claimAsking || id != "SELF" {
		t.Fatalf("first comment = %q, want ownership ask", posted[0].Body)
	}
	if kind, id, ok := parseClaim(posted[1].Body); !ok || kind != claimContinuing || id != "SELF" {
		t.Fatalf("second comment = %q, want continuing claim", posted[1].Body)
	}

	close(runner.release)
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
			logs := &syncBuffer{}
			w := &Glorp{
				Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
				Issues: &fakeSource{batches: [][]Issue{{{Number: 7}}}}, Runner: r, Out: logs,
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
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "https://github.com/users/o/projects/3", Interval: time.Millisecond, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: r, Out: logs,
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

func TestIssueBlockedWhenItHasSubIssues(t *testing.T) {
	blocked, reason := issueBlocked(Issue{HasSubIssues: true})
	if !blocked || reason != "has sub-issues" {
		t.Fatalf("blocked=%v reason=%q", blocked, reason)
	}
}

func TestGlorpDoesNotDispatchBlockedIssue(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{
		{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "open"}}},
	}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, Out: logs,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		src.mu.Lock()
		calls := src.calls
		src.mu.Unlock()
		if calls > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case got := <-runner.dispatched:
		cancel()
		<-done
		t.Fatalf("dispatched blocked issue #%d", got)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "issue #7 not picked up: depends on #12 (open)") {
		t.Fatalf("blocked issue was not logged:\n%s", logs.String())
	}
}

func TestGlorpDoesNotDispatchIssueWithSubIssues(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{
		{Number: 7, Repository: "o/r", HasSubIssues: true},
	}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: src, Runner: runner, Out: logs,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	select {
	case got := <-runner.dispatched:
		cancel()
		<-done
		t.Fatalf("dispatched issue with sub-issues #%d", got)
	default:
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "issue #7 not picked up: has sub-issues") {
		t.Fatalf("issue with sub-issues was not logged:\n%s", logs.String())
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
		// An organization board pushes every change over its own
		// projects_v2_item webhook, so probing it is duplicate polling
		// (issue #249).
		{name: "organization project target", targets: []string{"https://github.com/orgs/o/projects/3"}, useWebhooks: true, projects: &fakeProjectState{}, want: 0},
		// A repository-scoped project URL does not name the owner type, so
		// the probe stays on rather than assuming push coverage.
		{name: "repository-scoped project target", targets: []string{"https://github.com/o/r/projects/3"}, useWebhooks: true, projects: &fakeProjectState{}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := &Glorp{UseWebhooks: test.useWebhooks, Projects: test.projects}
			if got := len(w.projectProbeTargets(test.targets)); got != test.want {
				t.Fatalf("probed targets = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPushedBoardTargets(t *testing.T) {
	targets := []string{"o/r", "https://github.com/users/o/projects/3", "https://github.com/orgs/o/projects/4", "https://github.com/o/r/projects/5"}
	w := &Glorp{UseWebhooks: true, Projects: &fakeProjectState{}}
	got := w.pushedBoardTargets(targets)
	want := []string{"https://github.com/orgs/o/projects/4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pushed board targets = %v, want %v", got, want)
	}
	if got := (&Glorp{}).pushedBoardTargets(targets); got != nil {
		t.Fatalf("poll mode pushed board targets = %v, want none", got)
	}
	// Every project target is either probed or push-covered, never neither.
	probed := w.projectProbeTargets(targets)
	if len(probed)+len(got) != 3 {
		t.Fatalf("probed %v and pushed %v do not cover the 3 project targets", probed, got)
	}
}

// Push mode never polls at Interval, so saying so in the startup log makes a
// probed board look polled (issue #249).
func TestWatchDescription(t *testing.T) {
	if got := (&Glorp{Interval: time.Minute}).watchDescription(); got != "polling every 1m0s" {
		t.Fatalf("poll mode description = %q", got)
	}
	got := (&Glorp{Interval: time.Minute, UseWebhooks: true}).watchDescription()
	if !strings.Contains(got, pushFallbackInterval.String()) || strings.Contains(got, "1m0s") {
		t.Fatalf("push mode description = %q, want one naming the %s fallback and not the poll interval", got, pushFallbackInterval)
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

// runGlorpUntilAgentInvoked runs w until the fake agent stub records its first
// invocation, then stops the loop and returns that invocation.
func runGlorpUntilAgentInvoked(t *testing.T, w *Glorp, log string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	invoked := false
	for deadline := time.Now().Add(5 * time.Second); !invoked && time.Now().Before(deadline); {
		data, err := os.ReadFile(log)
		if invoked = err == nil && strings.Contains(string(data), "<<<END>>>"); !invoked {
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !invoked {
		t.Fatal("agent was never invoked")
	}
	invocations := fakeAgentInvocations(t, log)
	if len(invocations) != 1 {
		t.Fatalf("agent invocations = %#v, want exactly one", invocations)
	}
	return invocations[0]
}

func TestGlorpDiscardsPersistedAgentThatIsNoLongerConfigured(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	stale := workState{Status: "active", SessionID: "session-7", Agent: "codex", CheckoutDirectory: dir}
	if err := saveWorkState(statePath, map[int]workState{7: stale}); err != nil {
		t.Fatal(err)
	}
	binary, log := writeFakeAgent(t, "", 0)
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7}}}},
		Runner: CommandRunner{Agent: "claude", Agents: []string{"claude"}, ClaudeBinary: binary, CodexBinary: binary, Repo: "o/r"},
	}
	invocation := runGlorpUntilAgentInvoked(t, w, log)
	if strings.Contains(invocation, "--resume") || strings.Contains(invocation, "session-7") {
		t.Fatalf("invocation resumed the retired agent's session: %q", invocation)
	}
	if !strings.Contains(invocation, "--session-id") {
		t.Fatalf("invocation = %q, want a fresh claude session", invocation)
	}
	state, err := loadWorkState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state[7].Agent != "claude" {
		t.Fatalf("persisted agent = %q, want the currently configured claude", state[7].Agent)
	}
	if state[7].SessionID == "" || state[7].SessionID == stale.SessionID {
		t.Fatalf("persisted session ID = %q, want a new one", state[7].SessionID)
	}
}

func TestGlorpResumesPersistedAgentThatIsStillConfigured(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	want := workState{Status: "active", SessionID: "session-7", Agent: "codex", CheckoutDirectory: dir}
	if err := saveWorkState(statePath, map[int]workState{7: want}); err != nil {
		t.Fatal(err)
	}
	binary, log := writeFakeAgent(t, "", 0)
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: &fakeSource{batches: [][]Issue{{{Number: 7}}}},
		Runner: CommandRunner{Agent: "claude", Agents: []string{"claude", "codex"}, ClaudeBinary: binary, CodexBinary: binary, Repo: "o/r"},
	}
	invocation := runGlorpUntilAgentInvoked(t, w, log)
	if !strings.Contains(invocation, "resume") || !strings.Contains(invocation, "session-7") {
		t.Fatalf("invocation = %q, want the still configured codex session resumed", invocation)
	}
}

func TestGlorpAnswersOwnershipAskOnItsPullRequest(t *testing.T) {
	dir := t.TempDir()
	// The issue is worked locally as #7, but its draft pull request is #42, so
	// the handoff ask arrives naming #42 (issue #363).
	source := &fakeClosureSource{
		fakeSource: &fakeSource{batches: [][]Issue{{{Number: 7, Repository: "o/r"}}}},
		state:      OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 42, State: "open"}}},
	}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	events := make(chan WebhookEvent, 1)
	comments := newFakeCommentClient()
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: source, Runner: runner, UseWebhooks: true, Events: events,
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

	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 42, CommentBody: signComment(askClaimBody, "OTHER")}

	deadline := time.Now().Add(2 * time.Second)
	var posted []Comment
	for time.Now().Before(deadline) {
		var err error
		if posted, err = comments.ListComments(context.Background(), "o/r", 42); err != nil {
			t.Fatal(err)
		}
		if len(posted) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(posted) != 1 {
		t.Fatalf("pull request comments = %v, want a presence reply", posted)
	}
	if kind, id, ok := parseClaim(posted[0].Body); !ok || kind != claimPresence || id != "SELF" {
		t.Fatalf("posted comment = %q, want a presence claim from SELF", posted[0].Body)
	}

	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpIgnoresOwnershipAskOnAnotherInstancesPullRequest(t *testing.T) {
	dir := t.TempDir()
	source := &fakeClosureSource{
		fakeSource: &fakeSource{batches: [][]Issue{{{Number: 7, Repository: "o/r"}}}},
		// #42 was this issue's pull request, but it is merged, so an ask on it
		// is no longer about work in flight here.
		state: OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 42, State: "merged", Merged: true}}},
	}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	events := make(chan WebhookEvent, 1)
	comments := newFakeCommentClient()
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state.json"),
		Issues: source, Runner: runner, UseWebhooks: true, Events: events,
		fallbackInterval: time.Hour, Identity: "SELF", Comments: comments,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case <-runner.dispatched:
	case <-time.After(time.Second):
		t.Fatal("issue #7 was not dispatched")
	}

	events <- WebhookEvent{Kind: "issue_comment", Action: "created", Repository: "o/r", IssueNumber: 42, CommentBody: signComment(askClaimBody, "OTHER")}
	time.Sleep(200 * time.Millisecond)

	posted, err := comments.ListComments(context.Background(), "o/r", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) != 0 {
		t.Fatalf("pull request comments = %v, want silence for work this instance does not hold", posted)
	}

	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// alwaysFailingStatuser refuses every status move, standing in for the
// dispatch that is skipped after a handshake has already been won.
type alwaysFailingStatuser struct{}

func (alwaysFailingStatuser) SetIssueStatus(_ context.Context, _ string, _ Issue, _ string) error {
	return errors.New("status update failure")
}

// A won handshake posts a claim before the dispatch it announces happens, so a
// dispatch skipped afterwards must withdraw that claim rather than leave the
// ticket owned by an instance with no work record and no running agent
// (issue #434).
func TestGlorpReleasesClaimWhenDispatchIsSkippedAfterHandshake(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 7, Repository: "o/r", ProjectStatus: "In Progress"}}}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	comments := newFakeCommentClient()
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: time.Hour, Concurrency: 1,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: runner, Out: logs,
		Status: alwaysFailingStatuser{}, Comments: comments, Identity: "SELF",
		ownershipWait: func(context.Context) bool { return true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	var posted []Comment
	for time.Now().Before(deadline) {
		posted, _ = comments.ListComments(context.Background(), "o/r", 7)
		if len(posted) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case got := <-runner.dispatched:
		t.Fatalf("issue #%d was dispatched even though its status move failed", got)
	default:
	}
	if len(posted) != 3 {
		t.Fatalf("posted comments = %v, want the ask, the claim, and a withdrawal of that claim", posted)
	}
	if kind, id, ok := parseClaim(posted[2].Body); !ok || kind != claimReleasing || id != "SELF" {
		t.Fatalf("last comment = %q, want the claim withdrawn by SELF", posted[2].Body)
	}
	standing, err := w.claimStanding(context.Background(), ownershipTarget{Repo: "o/r", Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if standing.SelfHolds || standing.OwnerClaimed {
		t.Fatalf("claimStanding = %+v, want the ticket to read as unclaimed rather than claimed-but-idle", standing)
	}
	if record, _, ok := w.settledHandshake(ownershipTarget{Repo: "o/r", Number: 7}); ok {
		t.Fatalf("settledHandshake = %+v, want the record dropped so a later reap renegotiates", record)
	}
	requireLogged(t, logs.String(),
		"issue #7 not dispatched; failed to set project status",
		"issue o/r#7 released by SELF (\"Releasing this issue\"); setting the project status failed, so it was never dispatched",
	)

	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// The reap's grace window used to run on the poll loop, so a single contested
// ticket froze the whole watch for two minutes: poll #1 in the log of issue
// #437 started at 21:12:21 and completed at 21:14:25, and nothing was read or
// dispatched in between. Polling must continue while a handshake waits.
func TestGlorpKeepsPollingWhileAHandoffWaitsOutItsGraceWindow(t *testing.T) {
	dir := t.TempDir()
	// Issue #7 is stranded at "In Progress", so it is negotiated; issue #8
	// appears on the next poll and needs no handshake at all.
	src := &fakeSource{batches: [][]Issue{
		{{Number: 7, Repository: "o/r", ProjectStatus: "In Progress"}},
		{{Number: 7, Repository: "o/r", ProjectStatus: "In Progress"}, {Number: 8, Repository: "o/r", ProjectStatus: "Todo"}},
	}}
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 2)}
	comments := newFakeCommentClient()
	release := make(chan struct{})
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "https://github.com/o/r/projects/3", Interval: 20 * time.Millisecond, Concurrency: 2,
		StatePath: filepath.Join(dir, "state.json"), Issues: src, Runner: runner, Out: logs,
		Comments: comments, Identity: "SELF", ownershipWait: func(ctx context.Context) bool {
			select {
			case <-release:
				return true
			case <-ctx.Done():
				return false
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Issue #8 is discovered and dispatched while #7's handshake is still
	// blocked inside its grace window.
	select {
	case got := <-runner.dispatched:
		if got != 8 {
			t.Fatalf("dispatched issue #%d first, want #8 while #7 is still negotiating", got)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatalf("polling stopped while the handoff waited out its grace window:\n%s", logs.String())
	}

	// Every poll in that window leaves the in-flight handshake alone.
	posted, err := comments.ListComments(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("posted = %v, want only the single ask while the handshake waits", posted)
	}

	// Once it settles, the won ticket is dispatched without waiting for a tick.
	close(release)
	select {
	case got := <-runner.dispatched:
		if got != 7 {
			t.Fatalf("dispatched issue #%d, want #7 once its handshake settled", got)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatalf("the reclaimed issue was never dispatched:\n%s", logs.String())
	}

	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGlorpPublishesLastPollTime(t *testing.T) {
	reporter := &snapshotReporter{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: &fakeSource{batches: [][]Issue{{}}},
		Runner: fakeOutputRunner{}, UI: reporter,
	}

	before := time.Now()
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
	if len(reporter.snapshots) == 0 {
		t.Fatal("no snapshots were published")
	}
	last := reporter.snapshots[len(reporter.snapshots)-1]
	if last.LastPoll.Before(before) {
		t.Fatalf("last poll time %v was not recorded after the poll ran (started %v)", last.LastPoll, before)
	}
}
