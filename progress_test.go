package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDashboardShowsJobProgressBar(t *testing.T) {
	m := newDashboard(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{
		{Number: 7, Title: "planned", Status: "active", PullRequest: 187, TasksDone: 2, TasksTotal: 5, Progress: 40},
		{Number: 8, Title: "unplanned", Status: "active", PullRequest: 188},
	}}))
	view := updated.(dashboard).View()
	if want := "#7 · PR #187 [████░░░░░░] 40% planned"; !strings.Contains(view, want) {
		t.Errorf("dashboard missing %q:\n%s", want, view)
	}
	if want := "#8 · PR #188 unplanned"; !strings.Contains(view, want) {
		t.Errorf("dashboard missing %q:\n%s", want, view)
	}
	if strings.Count(view, "%") != 1 {
		t.Errorf("job without a checklist shows a progress bar:\n%s", view)
	}
}

func TestJobProgressBar(t *testing.T) {
	for _, test := range []struct {
		job  JobSnapshot
		want string
	}{
		{JobSnapshot{}, ""},
		{JobSnapshot{TasksTotal: 4, Progress: 0}, "[░░░░░░░░░░] 0%"},
		{JobSnapshot{TasksDone: 2, TasksTotal: 3, Progress: 66}, "[██████░░░░] 66%"},
		{JobSnapshot{TasksDone: 4, TasksTotal: 4, Progress: 100}, "[██████████] 100%"},
	} {
		if got := jobProgressBar(test.job); got != test.want {
			t.Errorf("jobProgressBar(%d/%d) = %q, want %q", test.job.TasksDone, test.job.TasksTotal, got, test.want)
		}
	}
}

func TestOriginatingWorkStateCountsPullRequestChecklist(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"state":"OPEN"}`),
		[]byte(`[{"event":"cross-referenced","source":{"issue":{"number":9,"body":"Closes #7","pull_request":{"merged_at":null}}}}]`),
		[]byte(`{"state":"open","draft":true,"body":"## Plan\n- [x] parse\n- [ ] render\n- [ ] test\n\nCloses #7"}`),
	}
	calls := 0
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		return responses[calls-1], nil
	}}
	state, err := gh.OriginatingWorkState(context.Background(), "owner/repo", 7)
	if err != nil || len(state.PullRequests) != 1 {
		t.Fatalf("OriginatingWorkState() = (%#v, %v)", state, err)
	}
	if got := state.PullRequests[0]; got.TasksDone != 1 || got.TasksTotal != 3 {
		t.Fatalf("pull request checklist = %d/%d, want 1/3", got.TasksDone, got.TasksTotal)
	}
}

func TestGlorpUpdatesJobProgressAsChecklistChanges(t *testing.T) {
	reporter := &snapshotReporter{}
	src := &fakeClosureSource{fakeSource: &fakeSource{batches: [][]Issue{{{Number: 7, Title: "bug"}}}}}
	runner := &fakeRunner{release: make(chan struct{})}
	w := &Glorp{
		Repo: "o/r", Interval: time.Hour, Concurrency: 1,
		Issues: src, Runner: runner, UI: reporter, closureInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		cancel()
		close(runner.release)
		<-done
	}()

	latest := func() JobSnapshot {
		reporter.mu.Lock()
		defer reporter.mu.Unlock()
		if len(reporter.snapshots) == 0 {
			return JobSnapshot{}
		}
		for _, job := range reporter.snapshots[len(reporter.snapshots)-1].Jobs {
			if job.Number == 7 {
				return job
			}
		}
		return JobSnapshot{}
	}
	checkpoint := func(tasksDone, tasksTotal, want int) {
		t.Helper()
		src.mu.Lock()
		src.state = OriginatingWorkState{IssueState: "OPEN", PullRequests: []PullRequestWorkState{{Repository: "o/r", Number: 9, State: "OPEN", IsDraft: true, TasksDone: tasksDone, TasksTotal: tasksTotal}}}
		src.mu.Unlock()
		deadline := time.Now().Add(webhookDispatchWait)
		for time.Now().Before(deadline) {
			if job := latest(); job.HasProgress() == (tasksTotal > 0) && job.Progress == want && job.TasksTotal == tasksTotal {
				return
			}
			time.Sleep(time.Millisecond)
		}
		job := latest()
		t.Fatalf("job progress = %d%% (%d/%d), want %d%% (%d/%d)", job.Progress, job.TasksDone, job.TasksTotal, want, tasksDone, tasksTotal)
	}

	// A draft pull request without a checklist yet shows no progress.
	checkpoint(0, 0, 0)
	checkpoint(1, 4, 25)
	checkpoint(2, 4, 50)
	// The plan grew at the next checkpoint, so the percentage drops.
	checkpoint(2, 5, 40)
}
