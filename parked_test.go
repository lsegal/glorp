package main

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
)

// waitForParked waits until issue number is recorded as parked work of kind,
// and returns its parked record (issue #732).
func waitForParked(t *testing.T, statePath string, number int, kind string) core.ParkedWork {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := loadWorkState(statePath)
		if record := state[number]; err == nil && record.Status == "parked" && record.Parked != nil && record.Parked.Kind == kind {
			return *record.Parked
		}
		if time.Now().After(deadline) {
			t.Fatalf("issue #%d was not parked as %s, state=%v err=%v", number, kind, state, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGlorpParksADependencyBlockedIssueUntilItsBlockerCloses(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	src := parkingSource(blockedOn12("open"))
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

	parked := waitForParked(t, statePath, 7, core.ParkedBlocked)
	if !slices.Equal(parked.WaitingOn, []int{12}) {
		t.Fatalf("parked record = %+v, want it waiting on #12", parked)
	}
	select {
	case extra := <-runner.sessions:
		t.Fatalf("a parked blocked issue took a slot while its blocker is open: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	if count := strings.Count(logs.String(), "issue #7 not picked up: depends on #12 (open)"); count != 1 {
		t.Fatalf("parking was logged %d time(s), want once:\n%s", count, logs)
	}

	src.mergeBlocker()
	waitForSession(t, runner.sessions)
	if !strings.Contains(logs.String(), "issue #7 unparked: its blockers are closed; dispatching it") {
		t.Fatalf("waking was not logged:\n%s", logs)
	}
}

func TestGlorpForgetsAParkedBlockedIssueThatClosed(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	src := parkingSource(blockedOn12("open"))
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

	waitForParked(t, statePath, 7, core.ParkedBlocked)
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
			t.Fatalf("closed parked issue was not forgotten, state=%v err=%v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// Woken parked work goes back through the claim protocol even though its
// session resumes, so when two instances see the same wake, the one that
// claimed first keeps it and the other stands down (issue #732).
func TestStandDownForStandingClaimsChecksWokenParkedWork(t *testing.T) {
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
