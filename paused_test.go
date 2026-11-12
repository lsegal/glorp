package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
)

// waitForPaused waits until issue number is recorded as paused work of kind,
// and returns its paused record (issue #732).
func waitForPaused(t *testing.T, statePath string, number int, kind string) core.PausedWork {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := loadWorkState(statePath)
		if record := state[number]; err == nil && record.Status == "paused" && record.Paused != nil && record.Paused.Kind == kind {
			return *record.Paused
		}
		if time.Now().After(deadline) {
			t.Fatalf("issue #%d was not paused as %s, state=%v err=%v", number, kind, state, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGlorpPausesADependencyBlockedIssueUntilItsBlockerCloses(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	src := pausingSource(blockedOn12("open"))
	// The blocker has no pull request to stack on, so the issue can only wait.
	src.states[12] = OriginatingWorkState{IssueState: "open"}
	runner := &finishingSessionRunner{agent: "claude", sessions: make(chan AgentSession, 4), finish: 1}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: 5 * time.Millisecond, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	paused := waitForPaused(t, statePath, 7, core.PausedBlocked)
	if !slices.Equal(paused.WaitingOn, []int{12}) {
		t.Fatalf("paused record = %+v, want it waiting on #12", paused)
	}
	select {
	case extra := <-runner.sessions:
		t.Fatalf("a paused blocked issue took a slot while its blocker is open: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	if count := strings.Count(logs.String(), "issue #7 not picked up: depends on #12 (open)"); count != 1 {
		t.Fatalf("pausing was logged %d time(s), want once:\n%s", count, logs)
	}

	src.mergeBlocker()
	waitForSession(t, runner.sessions)
	if !strings.Contains(logs.String(), "issue #7 unpaused: its blockers are closed; dispatching it") {
		t.Fatalf("waking was not logged:\n%s", logs)
	}
}

func TestGlorpForgetsAPausedBlockedIssueThatClosed(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	src := pausingSource(blockedOn12("open"))
	src.states[12] = OriginatingWorkState{IssueState: "open"}
	runner := &finishingSessionRunner{agent: "claude", sessions: make(chan AgentSession, 4), finish: 1}
	w := &Glorp{
		Repo: "o/r", Interval: 5 * time.Millisecond, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: io.Discard, closureInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitForPaused(t, statePath, 7, core.PausedBlocked)
	// The issue was closed: it drops out of the open issues the poll lists,
	// and reading it back says so.
	src.mu.Lock()
	src.states[7] = OriginatingWorkState{IssueState: "closed"}
	src.mu.Unlock()
	src.fakeSource.mu.Lock()
	src.batches = [][]Issue{{}}
	src.fakeSource.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := loadWorkState(statePath)
		if _, ok := state[7]; err == nil && !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed paused issue was not forgotten, state=%v err=%v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// Woken paused work goes back through the claim protocol even though its
// session resumes, so when two instances see the same wake, the one that
// claimed first keeps it and the other stands down (issue #732).
func TestStandDownForStandingClaimsChecksWokenPausedWork(t *testing.T) {
	comments := newFakeCommentClient()
	comments.inject("o/r", 1, Comment{Body: signComment(startingClaimBody, "OTHER"), CreatedAt: time.Now().Add(-time.Minute)})
	comments.inject("o/r", 2, Comment{Body: signComment(startingClaimBody, "OTHER"), CreatedAt: time.Now().Add(-2 * time.Minute)})
	comments.inject("o/r", 2, Comment{Body: signComment(startingClaimBody, "SELF"), CreatedAt: time.Now().Add(-time.Minute)})
	w := &Glorp{Comments: comments, Identity: "SELF", Out: io.Discard}
	pending := []pendingIssue{
		{issue: Issue{Number: 1, Repository: "o/r", Target: "o/r"}, session: AgentSession{Resume: true}, woken: true},
		{issue: Issue{Number: 2, Repository: "o/r", Target: "o/r"}, session: AgentSession{Resume: true}, woken: true},
	}
	result := w.standDownForStandingClaims(context.Background(), nil, pending, map[string]bool{})
	if len(result) != 1 || result[0].issue.Number != 2 {
		t.Fatalf("result = %+v, want the issue another instance claimed first (#1) stood down for and this instance's own claim (#2) kept", result)
	}
}

// A state file saved while paused work was called parked is read as paused,
// with its record kept (issue #734).
func TestLoadWorkStateReadsParkedWorkAsPaused(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
  "acme/app#7": {"status": "parked", "parked": {"kind": "blocked", "waitingOn": [3]}},
  "acme/app#8": {"status": "parked"}
}
`
	if err := os.WriteFile(statePath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := loadScopedWorkState(statePath, []string{"acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := state["acme/app#7"]
	if blocked.Status != "paused" || blocked.Paused == nil || blocked.Paused.Kind != core.PausedBlocked || !slices.Equal(blocked.Paused.WaitingOn, []int{3}) {
		t.Fatalf("blocked state = %+v, want paused blocked work waiting on #3", blocked)
	}
	if stacked := state["acme/app#8"]; stacked.Status != "paused" || pausedRecord(stacked).Kind != core.PausedStacked {
		t.Fatalf("stacked state = %+v, want paused stacked work", stacked)
	}
	if err := saveScopedWorkState(statePath, state, []string{"acme/app"}); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "parked") {
		t.Fatalf("saved state still says parked:\n%s", saved)
	}
}

// The nupp#5 shape (issue #762): a pull-request record saved while some pull
// request was held, for an issue that now has no pull request, no sub-issues,
// and only closed dependencies. Nothing is left for it to wait on, so the
// first poll dispatches it instead of leaving it paused forever.
func TestGlorpDispatchesPausedPullRequestWorkWithNothingToWaitOn(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	saved := `{"o/r#7": {"status": "paused", "paused": {"kind": "pull-request", "pullRequests": [15]}}}`
	if err := os.WriteFile(statePath, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	src := pausingSource(blockedOn12("closed"))
	src.states[7] = OriginatingWorkState{IssueState: "open"}
	runner := &finishingSessionRunner{agent: "claude", sessions: make(chan AgentSession, 4), finish: 1}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: 5 * time.Millisecond, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitForSession(t, runner.sessions)
	if !strings.Contains(logs.String(), "issue #7 unpaused: it has no open pull request and nothing blocks it; dispatching it") {
		t.Fatalf("waking was not logged:\n%s", logs)
	}
}

// Retrying a paused job from the web UI clears its pause and reruns gh-fix,
// whatever kind of work it was waiting on, leaving its session as a failed
// run's is (issue #763).
func TestGlorpWebUIRetriesPausedWork(t *testing.T) {
	cases := []struct {
		name   string
		paused string
		issues []Issue
		own    OriginatingWorkState
	}{
		{"blocked", `{"kind": "blocked", "waitingOn": [12]}`, blockedOn12("open"), OriginatingWorkState{IssueState: "open"}},
		{"stacked", `{"kind": "stacked", "waitingOn": [12], "pullRequests": [15]}`, blockedOn12("open"), OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "open", Stacked: true}}}},
		{"pull request", `{"kind": "pull-request", "pullRequests": [15]}`, []Issue{{Number: 7, Repository: "o/r"}}, OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "open"}}}},
		{"legacy", ``, blockedOn12("open"), OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "open", Stacked: true}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.json")
			record := `"status": "paused", "sessionId": "session-7", "agent": "claude"`
			if tc.paused != "" {
				record += `, "paused": ` + tc.paused
			}
			if err := os.WriteFile(statePath, []byte(`{"o/r#7": {`+record+`}}`), 0600); err != nil {
				t.Fatal(err)
			}
			src := pausingSource(tc.issues)
			src.states[7] = tc.own
			// The blocker has no pull request to stack on, so only the retry
			// can move the job.
			src.states[12] = OriginatingWorkState{IssueState: "open"}
			// The rerun keeps running, so its active state can be read back.
			runner := &finishingSessionRunner{agent: "claude", sessions: make(chan AgentSession, 4)}
			logs := &syncBuffer{}
			w := &Glorp{
				Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
				Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- w.Run(ctx) }()
			defer func() {
				cancel()
				<-done
			}()

			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(logs.String(), "issue #7 paused: ") {
				if time.Now().After(deadline) {
					t.Fatalf("the restored work was not left paused:\n%s", logs)
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case extra := <-runner.sessions:
				t.Fatalf("paused work ran before it was retried: %+v", extra)
			default:
			}

			if err := w.handleJobAction(ctx, jobAction{Action: "retry", Target: "o/r", Number: 7}); err != nil {
				t.Fatalf("retrying paused work failed: %v\n%s", err, logs)
			}
			session := waitForSession(t, runner.sessions)
			if !session.Resume || session.ID != "session-7" {
				t.Fatalf("session = %+v, want the paused work's session resumed", session)
			}
			waitForWorkStatus(t, statePath, 7, "active")
			if !strings.Contains(logs.String(), "issue #7 retry requested from web UI; clearing its pause and rerunning gh-fix") {
				t.Fatalf("the retry was not logged:\n%s", logs)
			}
		})
	}
}

// A retry that cannot requeue the paused job keeps it paused and says so,
// rather than reporting success for a retry that changed nothing (issue #763).
func TestGlorpWebUIRetryKeepsPauseWhenNothingIsRequeued(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`{"o/r#7": {"status": "paused", "sessionId": "session-7", "agent": "claude", "paused": {"kind": "blocked", "waitingOn": [12]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	src := pausingSource(blockedOn12("open"), []Issue{})
	src.states[12] = OriginatingWorkState{IssueState: "open"}
	runner := &finishingSessionRunner{agent: "claude", sessions: make(chan AgentSession, 4), finish: 1}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: statePath,
		Issues: src, Runner: runner, Out: logs, closureInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "issue #7 paused: ") {
		if time.Now().After(deadline) {
			t.Fatalf("the restored work was not left paused:\n%s", logs)
		}
		time.Sleep(time.Millisecond)
	}
	// The retry's poll no longer lists the issue, so nothing can rerun it.
	err := w.handleJobAction(ctx, jobAction{Action: "retry", Target: "o/r", Number: 7})
	if err == nil || !strings.Contains(err.Error(), "stays paused") {
		t.Fatalf("retry error = %v, want it to report the job stays paused", err)
	}
	waitForPaused(t, statePath, 7, core.PausedBlocked)
	select {
	case extra := <-runner.sessions:
		t.Fatalf("a retry that was refused still ran: %+v", extra)
	default:
	}
}
