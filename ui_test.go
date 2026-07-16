package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDashboardShowsStatusAndTargets(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(dashboard)
	updated, _ = m.Update(snapshotMsg(WatchSnapshot{
		Targets: []string{"o/one"}, IssueCounts: map[string]int{"o/one": 4},
		Concurrency: 3, Running: 1, Queued: 1, Completed: 2, UseWebhooks: true,
		WebhookURL: "https://robot.example/webhook", WebhookOnline: true,
		TokensUsed: 12, TokenLimit: 100,
		Jobs: []JobSnapshot{{Number: 7, Title: "Improve UI", Status: "active", Started: time.Now()}},
	}))
	view := updated.(dashboard).View()
	for _, want := range []string{"idle", "active", "total", "12/100", "push", "o/one (4 issues)", "#7", "Improve UI", "Logs"} {
		if !strings.Contains(view, want) {
			t.Errorf("dashboard missing %q in %q", want, view)
		}
	}
}

func TestDashboardUsesScrollableAgentViewport(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(WatchSnapshot{Jobs: []JobSnapshot{{Number: 7, Title: "UI", Status: "active", Log: "line 1\nline 2\nline 3\nline 4\nline 5"}}}))
	m = updated.(dashboard)
	if _, ok := m.jobs[7]; !ok {
		t.Fatal("agent viewport was not created")
	}
	if m.jobs[7].Height != jobCardHeight-4 {
		t.Fatalf("agent viewport height = %d, want %d", m.jobs[7].Height, jobCardHeight-4)
	}
	if strings.Contains(m.View(), "Agent 7") {
		t.Fatal("agent prefix should not appear in the job title")
	}
}

func TestStatusBarUsesRaisedBackground(t *testing.T) {
	view := renderStatusBar([]string{"jobs"})
	if strings.Contains(view, "┏") || strings.Contains(view, "╋") {
		t.Fatalf("status bar should not use borders: %q", view)
	}
}

func TestDashboardKeepsLogsInDedicatedPanel(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(dashboard)
	updated, _ = m.Update(logMsg("webhook delivered"))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "Logs") || !strings.Contains(view, "webhook delivered") {
		t.Fatalf("dashboard missing log panel content: %s", view)
	}
	if updated.(dashboard).viewport.Height != 7 {
		t.Fatalf("log viewport height = %d, want 7", updated.(dashboard).viewport.Height)
	}
}

func TestDashboardShowsQuota(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(WatchSnapshot{Quota: "weekly 87% left"}))
	if !strings.Contains(updated.(dashboard).View(), "quota: weekly 87% left") {
		t.Fatal("dashboard did not show quota")
	}
}

func TestFormatCodexQuota(t *testing.T) {
	window := int64(7 * 24 * 60)
	if got := formatCodexQuota(&codexPrimaryRateLimit{UsedPercent: 13, WindowDurationMins: &window}); got != "weekly 87% left" {
		t.Fatalf("quota = %q", got)
	}
}

func TestCodexRateLimitsRequestOmitsParams(t *testing.T) {
	requests := codexQuotaRequests()
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal([]byte(requests[len(requests)-1]), &request); err != nil {
		t.Fatal(err)
	}
	if request["method"] == nil || string(request["method"]) != `"account/rateLimits/read"` {
		t.Fatalf("last request method = %s", request["method"])
	}
	if _, ok := request["params"]; ok {
		t.Fatal("account/rateLimits/read request must omit params")
	}
}

func TestDashboardTrimsOldestJobs(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(dashboard)
	jobs := make([]JobSnapshot, 10)
	for i := range jobs {
		jobs[i] = JobSnapshot{Number: i + 1, Title: "job", Status: "complete", Started: time.Unix(int64(i), 0)}
	}
	updated, _ = m.Update(snapshotMsg(WatchSnapshot{Concurrency: 3, Jobs: jobs}))
	view := updated.(dashboard).View()
	if strings.Contains(view, "#1 ") || strings.Contains(view, "#4 ") || !strings.Contains(view, "#10 ") {
		t.Fatalf("dashboard did not keep newest six jobs: %s", view)
	}
}

func TestDashboardUsesTwoByThreeAgentGrid(t *testing.T) {
	if maxVisibleJobs != 6 || jobGridColumns != 2 || jobCardHeight != 8 {
		t.Fatalf("agent grid = %d jobs, %d columns, card height %d; want 6, 2, 8", maxVisibleJobs, jobGridColumns, jobCardHeight)
	}
}

func TestFormatTargets(t *testing.T) {
	got := formatTargets([]string{"o/one", "o/two"}, map[string]int{"o/one": 2})
	if got[0] != "o/one (2 issues)" || got[1] != "o/two (0 issues)" {
		t.Fatalf("targets = %v", got)
	}
}
