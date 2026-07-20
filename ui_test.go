package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestDashboardShowsStatusAndTargets(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(dashboard)
	updated, _ = m.Update(snapshotMsg(GlorpSnapshot{
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
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{Number: 7, Title: "UI", Status: "active", Log: "line 1\nline 2\nline 3\nline 4\nline 5"}}}))
	m = updated.(dashboard)
	if _, ok := m.jobs[7]; !ok {
		t.Fatal("agent viewport was not created")
	}
	if m.jobs[7].Height != 8 {
		t.Fatalf("agent viewport height = %d, want 8", m.jobs[7].Height)
	}
	if strings.Contains(m.View(), "Agent 7") {
		t.Fatal("agent prefix should not appear in the job title")
	}
	for _, line := range []string{"line 1", "line 2", "line 3", "line 4", "line 5"} {
		if !strings.Contains(m.View(), line) {
			t.Errorf("dashboard missing visible agent output %q: %s", line, m.View())
		}
	}
}

func TestDashboardShowsProgressInsteadOfJobStatus(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{
		{Number: 7, Title: "UI", Status: "active", Log: "working on it"},
	}}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "working on it") {
		t.Fatalf("dashboard did not show job progress: %s", view)
	}
	card := strings.Split(view, "Logs")[0]
	if strings.Contains(card, "active") {
		t.Fatalf("dashboard rendered the job status: %s", view)
	}
}

func TestDashboardKeepsAgentMetadataVisibleForEveryStatus(t *testing.T) {
	for _, status := range []string{"active", "failed", "complete"} {
		t.Run(status, func(t *testing.T) {
			m := newDashboard()
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
				Number: 7, Title: "UI", Status: status,
				CheckoutDirectory: "/tmp/glorp-gh-fix-126", SessionID: "session-126",
			}}}))
			view := updated.(dashboard).View()
			lines := strings.Split(view, "\n")
			if len(lines) < 3 || !strings.Contains(lines[0], "UI") || strings.TrimSpace(lines[1]) != "checkout: /tmp/glorp-gh-fix-126" || strings.TrimSpace(lines[2]) != "session: session-126" {
				t.Fatalf("dashboard did not show agent metadata on separate lines for %s job: %s", status, view)
			}
		})
	}
}

func TestDashboardShowsCheckmarkForCompletedJob(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{
		{Number: 7, Title: "UI", Status: "complete", Log: "finished"},
	}}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "✓") {
		t.Fatalf("dashboard did not show completion checkmark: %s", view)
	}
	card := strings.Split(view, "Logs")[0]
	if strings.Contains(card, "complete") || strings.Contains(card, "finished") {
		t.Fatalf("dashboard rendered the completed status or stale progress: %s", view)
	}
	for _, line := range strings.Split(card, "\n") {
		if strings.Contains(line, "#7") && !strings.Contains(line, "✓") {
			t.Fatalf("dashboard rendered the completion checkmark on a separate line: %s", view)
		}
	}
}

func TestDashboardTruncatesAgentTitleToCardWidth(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "This is a deliberately long agent issue title", Status: "active",
	}}}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "This is a deliberately long agent") {
		t.Fatalf("dashboard did not use the available card width for the title: %s", view)
	}
	if strings.Contains(view, "This is a deliber...") {
		t.Fatalf("dashboard still uses the old fixed title width: %s", view)
	}
}

func TestTruncateKeepsDisplayWidthWithinLimit(t *testing.T) {
	for _, test := range []struct {
		input string
		width int
	}{
		{input: "a long title", width: 8},
		{input: "日本語のタイトル", width: 8},
	} {
		if got := truncate(test.input, test.width); lipgloss.Width(got) > test.width {
			t.Errorf("truncate(%q, %d) = %q with display width %d", test.input, test.width, got, lipgloss.Width(got))
		}
	}
}

func TestStatusBarUsesRaisedBackground(t *testing.T) {
	view := renderStatusBar([]string{"jobs"})
	if strings.Contains(view, "┏") || strings.Contains(view, "╋") {
		t.Fatalf("status bar should not use borders: %q", view)
	}
}

func TestDashboardUsesDarkBorderlessViewportPanels(t *testing.T) {
	if panel.GetBackground() != lipgloss.Color("236") || logPanel.GetBackground() != lipgloss.Color("236") {
		t.Fatalf("viewport backgrounds = %q and %q, want dark gray", panel.GetBackground(), logPanel.GetBackground())
	}
	if panel.GetBorderStyle().Top != "" || logPanel.GetBorderStyle().Top != "" {
		t.Fatal("viewport panels should not have borders")
	}
}

func TestStatusBarUsesDistinctBackgrounds(t *testing.T) {
	if len(statusBars) < 2 {
		t.Fatal("status bar should define multiple background styles")
	}
	for i := 1; i < len(statusBars); i++ {
		if statusBars[i].GetBackground() == statusBars[0].GetBackground() {
			t.Fatalf("status bar cell %d reuses the first cell background", i)
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
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Quota: "weekly 87% left"}))
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
	updated, _ = m.Update(snapshotMsg(GlorpSnapshot{Concurrency: 3, Jobs: jobs}))
	view := updated.(dashboard).View()
	if strings.Contains(view, "#1 ") || strings.Contains(view, "#4 ") || !strings.Contains(view, "#10 ") {
		t.Fatalf("dashboard did not keep newest six jobs: %s", view)
	}
}

func TestDashboardUsesTwoByThreeAgentGrid(t *testing.T) {
	if maxVisibleJobs != 6 || jobGridColumns != 2 || jobCardHeight != 12 {
		t.Fatalf("agent grid = %d jobs, %d columns, card height %d; want 6, 2, 12", maxVisibleJobs, jobGridColumns, jobCardHeight)
	}
}

func TestDashboardAddsGuttersBetweenSections(t *testing.T) {
	if dashboardGap != 1 {
		t.Fatalf("dashboard gap = %d, want 1", dashboardGap)
	}
	if got := joinHorizontalWithGap([]string{"one", "two"}, 1); got != "one two" {
		t.Fatalf("horizontal gap = %q, want %q", got, "one two")
	}
	if got := strings.Split(joinVerticalWithGap([]string{"one", "two"}, 1), "\n"); len(got) != 3 || got[0] != "one" || got[2] != "two" {
		t.Fatalf("vertical gap = %q, want one blank line", strings.Join(got, "\\n"))
	}
}

func TestStatusBarCountsAreOneSelfContainedCell(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Concurrency: 3, Running: 1, Queued: 1, Completed: 2}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "jobs: 1 idle 2 active 4 total") {
		t.Fatalf("dashboard status counts were not rendered as one cell: %s", view)
	}
}

func TestStatusBarCountStyles(t *testing.T) {
	if idleCountStyle.GetForeground() != lipgloss.Color("241") {
		t.Fatalf("idle count color = %q, want muted", idleCountStyle.GetForeground())
	}
	if activeCountStyle.GetForeground() != lipgloss.Color("42") {
		t.Fatalf("active count color = %q, want active", activeCountStyle.GetForeground())
	}
	if totalCountStyle.GetForeground() != lipgloss.Color("205") {
		t.Fatalf("total count color = %q, want bar", totalCountStyle.GetForeground())
	}
	for _, style := range []lipgloss.Style{countLabelStyle, idleCountStyle, activeCountStyle, totalCountStyle} {
		if style.GetBackground() != lipgloss.Color("24") {
			t.Fatalf("count cell background = %q, want status-bar background", style.GetBackground())
		}
	}
	if statusBars[0].GetPaddingLeft() != 0 || statusBars[0].GetPaddingRight() != 0 {
		t.Fatal("job status cell padding must be rendered with the count label style")
	}
}

func TestJobCountCellBackgroundCoversEveryCharacter(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })

	cell := renderStatusBar([]string{renderJobCounts(GlorpSnapshot{
		Concurrency: 3,
		Running:     1,
		Completed:   2,
	})})
	backgroundActive := false
	for i := 0; i < len(cell); {
		if cell[i] == '\x1b' {
			end := strings.IndexByte(cell[i:], 'm')
			if end < 0 {
				t.Fatalf("unterminated ANSI sequence in %q", cell)
			}
			sequence := cell[i+2 : i+end]
			if sequence == "0" {
				backgroundActive = false
			}
			if strings.Contains(sequence, "48;5;24") {
				backgroundActive = true
			}
			i += end + 1
			continue
		}
		if !backgroundActive {
			t.Fatalf("job count character %q rendered without its background in %q", cell[i], cell)
		}
		i++
	}
}

func TestFormatTargets(t *testing.T) {
	got := formatTargets([]string{"o/one", "o/two"}, map[string]int{"o/one": 2})
	if got[0] != "o/one (2 issues)" || got[1] != "o/two (0 issues)" {
		t.Fatalf("targets = %v", got)
	}
}
