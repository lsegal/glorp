package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStackSource is a closure source whose repository reports whether
// stacked pull requests are enabled (issue #657).
type fakeStackSource struct {
	*fakeClosureSource
	enabled bool
	probes  int
	mu      sync.Mutex
}

func (f *fakeStackSource) StacksEnabled(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	return f.enabled, nil
}

func TestStackableDependencyNeedsExactlyOneOpenBlocker(t *testing.T) {
	for _, test := range []struct {
		name  string
		issue Issue
		want  int
	}{
		{"one open blocker", Issue{DependsOn: []IssueDependency{{Number: 4, State: "open"}, {Number: 5, State: "CLOSED"}}}, 4},
		{"two open blockers", Issue{DependsOn: []IssueDependency{{Number: 4, State: "open"}, {Number: 5, State: "open"}}}, 0},
		{"tracking issue", Issue{HasSubIssues: true, DependsOn: []IssueDependency{{Number: 4, State: "open"}}}, 0},
		{"no open blockers", Issue{DependsOn: []IssueDependency{{Number: 4, State: "closed"}}}, 0},
	} {
		dependency, ok := stackableDependency(test.issue)
		if got := map[bool]int{true: dependency.Number}[ok]; got != test.want {
			t.Errorf("%s: stackableDependency() = %d, %v; want %d", test.name, dependency.Number, ok, test.want)
		}
	}
}

func TestGHCLIStacksEnabled(t *testing.T) {
	for _, test := range []struct {
		name    string
		output  string
		err     error
		want    bool
		wantErr bool
	}{
		{"enabled", `[]`, nil, true, false},
		{"not enabled", `{"message":"Not Found"}` + "\ngh: Not Found (HTTP 404)", errors.New("exit status 1"), false, false},
		{"failure", "gh: Server Error (HTTP 500)", errors.New("exit status 1"), false, true},
	} {
		var args []string
		gh := GHCLI{runCommand: func(_ context.Context, a ...string) ([]byte, error) {
			args = a
			return []byte(test.output), test.err
		}}
		got, err := gh.StacksEnabled(context.Background(), "owner/repo")
		if got != test.want || (err != nil) != test.wantErr {
			t.Errorf("%s: StacksEnabled() = %v, %v; want %v, error %v", test.name, got, err, test.want, test.wantErr)
		}
		if strings.Join(args, " ") != "api repos/owner/repo/stacks?per_page=1" {
			t.Errorf("%s: gh args = %q", test.name, args)
		}
	}
}

func runBlockedIssuePoll(t *testing.T, source IssueSource) (int, string) {
	t.Helper()
	runner := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 1)}
	logs := &syncBuffer{}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(t.TempDir(), "state.json"),
		Issues: source, Runner: runner, Out: logs,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	got := 0
	select {
	case got = <-runner.dispatched:
	case <-time.After(200 * time.Millisecond):
	}
	close(runner.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return got, logs.String()
}

func blockedIssueSource(enabled bool, pullRequests ...PullRequestWorkState) *fakeStackSource {
	return &fakeStackSource{
		fakeClosureSource: &fakeClosureSource{
			fakeSource: &fakeSource{batches: [][]Issue{{
				{Number: 7, Repository: "o/r", DependsOn: []IssueDependency{{Number: 12, State: "open"}}},
			}}},
			state: OriginatingWorkState{IssueState: "open", PullRequests: pullRequests},
		},
		enabled: enabled,
	}
}

func TestGlorpStacksBlockedIssueOnBlockerPullRequest(t *testing.T) {
	got, logs := runBlockedIssuePoll(t, blockedIssueSource(true, PullRequestWorkState{Number: 13, State: "closed"}, PullRequestWorkState{Number: 14, State: "open"}))
	if got != 7 {
		t.Fatalf("dispatched #%d, want blocked issue #7 stacked on its blocker\n%s", got, logs)
	}
	if !strings.Contains(logs, "issue #7 depends on #12 (open); stacking it on pull request #14") {
		t.Fatalf("stacked dispatch was not logged:\n%s", logs)
	}
}

func TestGlorpWaitsForBlockerWithoutStackedPullRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		source *fakeStackSource
	}{
		{"stacks disabled", blockedIssueSource(false, PullRequestWorkState{Number: 14, State: "open"})},
		{"blocker has no open pull request", blockedIssueSource(true, PullRequestWorkState{Number: 14, State: "closed", Merged: false})},
	} {
		got, logs := runBlockedIssuePoll(t, test.source)
		if got != 0 {
			t.Errorf("%s: dispatched blocked issue #%d", test.name, got)
		}
		if !strings.Contains(logs, "issue #7 not picked up: depends on #12 (open)") {
			t.Errorf("%s: blocked issue was not logged:\n%s", test.name, logs)
		}
	}
}

func TestGlorpCachesStackedPullRequestSetting(t *testing.T) {
	source := blockedIssueSource(false)
	w := &Glorp{Issues: source}
	for range 3 {
		if w.stacksEnabled(context.Background(), "o/r") {
			t.Fatal("stacksEnabled() = true, want false")
		}
	}
	if source.probes != 1 {
		t.Fatalf("StacksEnabled probes = %d, want 1 cached probe", source.probes)
	}
}

func TestGhFixStacksOnOpenBlockingIssue(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"## Stack on an open blocking issue",
		"gh api repos/<OWNER>/<REPO>/stacks?per_page=1",
		"exactly one open blocker",
		"gh extension install github/gh-stack",
		"gh stack link",
		"gh stack rebase --upstack",
		"Never commit to, rebase, push, or merge it",
		"never merge the stack with `gh stack merge`",
		"Never merge a stacked pull request while its base is anything other than the default branch",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not describe stacking %q", required)
		}
	}
}
