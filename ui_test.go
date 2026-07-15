package main

import (
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
	for _, want := range []string{"idle", "active", "total", "12/100", "push", "o/one (4 issues)", "#7", "Agent 7", "Logs"} {
		if !strings.Contains(view, want) {
			t.Errorf("dashboard missing %q in %q", want, view)
		}
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
	if strings.Contains(view, "#1 ") || !strings.Contains(view, "#10 ") {
		t.Fatalf("dashboard did not keep newest nine jobs: %s", view)
	}
}

func TestFormatTargets(t *testing.T) {
	got := formatTargets([]string{"o/one", "o/two"}, map[string]int{"o/one": 2})
	if got[0] != "o/one (2 issues)" || got[1] != "o/two (0 issues)" {
		t.Fatalf("targets = %v", got)
	}
}
