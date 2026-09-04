package main

import (
	"github.com/lsegal/glorp/webui"

	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

func TestDashboardFollowsStreamingAgentOutput(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "UI", Status: "active", Log: strings.Join([]string{
			"line 1", "line 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "line 9", "latest progress",
		}, "\n"),
	}}}))
	m = updated.(dashboard)
	if !m.jobs[7].AtBottom() || !strings.Contains(m.View(), "latest progress") {
		t.Fatalf("dashboard did not follow the latest agent output: %s", m.View())
	}
}

func TestDashboardPausesAgentAutoscrollAndMoreReturnsToBottom(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	log := numberedLines(14)
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "UI", Status: "active", Log: log,
	}}}))
	m = updated.(dashboard)
	region, ok := m.regionFor(viewportTarget{jobNumber: 7})
	if !ok {
		t.Fatal("agent viewport region was not found")
	}
	updated, _ = m.Update(tea.MouseMsg{X: region.x, Y: region.y, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = updated.(dashboard)
	pausedOffset := m.jobs[7].YOffset
	updated, _ = m.Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "UI", Status: "active", Log: log + "\nnew output",
	}}}))
	m = updated.(dashboard)
	if m.jobs[7].YOffset != pausedOffset || m.jobs[7].AtBottom() {
		t.Fatalf("agent viewport resumed autoscroll: offset = %d, want %d", m.jobs[7].YOffset, pausedOffset)
	}
	if !strings.Contains(m.View(), moreIndicator) {
		t.Fatalf("paused agent viewport did not show %q: %s", moreIndicator, m.View())
	}
	region, _ = m.regionFor(viewportTarget{jobNumber: 7})
	updated, _ = m.Update(tea.MouseMsg{
		X: region.contentEnd - moreIndicatorWidth, Y: region.y + region.height - 1,
		Button: tea.MouseButtonLeft, Action: tea.MouseActionPress,
	})
	m = updated.(dashboard)
	if !m.jobs[7].AtBottom() {
		t.Fatal("clicking the more indicator did not return the agent viewport to the bottom")
	}
}

func TestDashboardLogViewportBottomLock(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(dashboard)
	for i := 0; i < 12; i++ {
		updated, _ = m.Update(logMsg(fmt.Sprintf("log %d", i)))
		m = updated.(dashboard)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("log viewport did not follow appended output")
	}
	region, _ := m.regionFor(viewportTarget{logs: true})
	updated, _ = m.Update(tea.MouseMsg{X: region.x, Y: region.y, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = updated.(dashboard)
	pausedOffset := m.viewport.YOffset
	updated, _ = m.Update(logMsg("new output"))
	m = updated.(dashboard)
	if m.viewport.YOffset != pausedOffset || m.viewport.AtBottom() {
		t.Fatalf("log viewport resumed autoscroll: offset = %d, want %d", m.viewport.YOffset, pausedOffset)
	}
}

func TestDashboardMouseWheelOnlyScrollsViewportUnderPointer(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{
		{Number: 7, Title: "first", Status: "active", Started: time.Unix(2, 0), Log: numberedLines(14)},
		{Number: 8, Title: "second", Status: "active", Started: time.Unix(1, 0), Log: numberedLines(14)},
	}}))
	m = updated.(dashboard)
	firstBefore, secondBefore, logsBefore := m.jobs[7].YOffset, m.jobs[8].YOffset, m.viewport.YOffset
	region, _ := m.regionFor(viewportTarget{jobNumber: 7})
	updated, _ = m.Update(tea.MouseMsg{X: region.x, Y: region.y, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m = updated.(dashboard)
	if m.jobs[7].YOffset >= firstBefore {
		t.Fatal("pointed agent viewport did not scroll up")
	}
	if m.jobs[8].YOffset != secondBefore || m.viewport.YOffset != logsBefore {
		t.Fatal("mouse wheel scrolled a viewport that was not under the pointer")
	}
}

func TestDashboardMouseRegionsAlignWithRenderedScrollbars(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{
		{Number: 7, Title: "first", Status: "active", Started: time.Unix(2, 0), Log: numberedLines(14)},
		{Number: 8, Title: "second", Status: "active", Started: time.Unix(1, 0), Log: numberedLines(14)},
	}}))
	m = updated.(dashboard)
	updated, _ = m.Update(logMsg("dashboard ready"))
	m = updated.(dashboard)
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for _, target := range []viewportTarget{{jobNumber: 7}, {jobNumber: 8}, {logs: true}} {
		region, ok := m.regionFor(target)
		if !ok || region.y >= len(lines) {
			t.Fatalf("viewport region %+v is outside the rendered dashboard", target)
		}
		runes := []rune(lines[region.y])
		if region.contentEnd >= len(runes) || (runes[region.contentEnd] != '│' && runes[region.contentEnd] != '█') {
			t.Fatalf("viewport region %+v points to %q instead of its rendered scrollbar", target, lines[region.y])
		}
	}
}

func TestDashboardScrollbarCanBeDragged(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "UI", Status: "active", Log: numberedLines(30),
	}}}))
	m = updated.(dashboard)
	region, _ := m.regionFor(viewportTarget{jobNumber: 7})
	updated, _ = m.Update(tea.MouseMsg{X: region.contentEnd, Y: region.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(dashboard)
	if !m.jobs[7].AtTop() || m.dragging == nil {
		t.Fatal("pressing the top of the scrollbar did not start a drag at the top")
	}
	updated, _ = m.Update(tea.MouseMsg{X: region.contentEnd, Y: region.y + region.height - 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	m = updated.(dashboard)
	if !m.jobs[7].AtBottom() {
		t.Fatal("dragging the scrollbar to the last row did not reach the bottom")
	}
	updated, _ = m.Update(tea.MouseMsg{X: region.contentEnd, Y: region.y + region.height - 1, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease})
	if updated.(dashboard).dragging != nil {
		t.Fatal("releasing the scrollbar did not stop the drag")
	}
}

func TestRenderViewportAlwaysShowsScrollbar(t *testing.T) {
	view := viewport.New(10, 3)
	view.SetContent("one line")
	rendered := renderViewport(view)
	if !strings.Contains(rendered, "█") {
		t.Fatalf("unscrollable viewport did not render a scrollbar: %q", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if lipgloss.Width(line) != 11 {
			t.Fatalf("viewport line width = %d, want 11: %q", lipgloss.Width(line), line)
		}
	}
}

func numberedLines(count int) string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	return strings.Join(lines, "\n")
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
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "Preserve agent scrollback", Status: "active", Log: numberedLines(14),
		CheckoutDirectory: "/tmp/glorp-gh-fix-161", SessionID: "session-161",
	}}}))
	m = updated.(dashboard)
	view := m.jobs[7]
	view.LineUp(2)
	m.jobs[7] = view
	pausedOffset := view.YOffset
	updated, _ = m.Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{
		{Number: 7, Title: "Preserve agent scrollback", Status: "complete", Log: numberedLines(14),
			CheckoutDirectory: "/tmp/glorp-gh-fix-161", SessionID: "session-161"},
	}}))
	m = updated.(dashboard)
	rendered := m.View()
	if !strings.Contains(rendered, "✓") {
		t.Fatalf("dashboard did not show completion checkmark: %s", rendered)
	}
	card := strings.Split(rendered, "Logs")[0]
	if strings.Contains(card, "complete") || !strings.Contains(card, "line 5") {
		t.Fatalf("dashboard rendered the completed status or cleared its scrollback: %s", rendered)
	}
	if m.jobs[7].YOffset != pausedOffset {
		t.Fatalf("completed viewport offset = %d, want %d", m.jobs[7].YOffset, pausedOffset)
	}
	if _, ok := m.regionFor(viewportTarget{jobNumber: 7}); !ok {
		t.Fatal("completed agent viewport is not scrollable")
	}
	for _, line := range strings.Split(card, "\n") {
		if strings.Contains(line, "#7") && !strings.Contains(line, "✓") {
			t.Fatalf("dashboard rendered the completion checkmark on a separate line: %s", rendered)
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
	view := renderStatusBar(80, []string{"jobs"})
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

func TestDashboardShowsAllNamedQuotas(t *testing.T) {
	m := newDashboard()
	// Wide enough that the whole quota cell fits: the status bar truncates
	// rather than wraps (issue #489), so a narrower terminal would shorten
	// this list rather than show all of it.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Quotas: map[string]string{"codex": "weekly 87% left", "claude": ""}}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "quota: claude: not tracked, codex: weekly 87% left") {
		t.Fatalf("dashboard did not show all named quotas: %s", view)
	}
}

// TestDashboardTruncatesTheStatusBarWithManyAgents checks a run configured
// with four agents keeps the status bar on one line. The quota cell grows with
// every agent, and a bar wider than the terminal used to wrap onto a second
// line and shove the job grid up the screen.
func TestDashboardTruncatesTheStatusBarWithManyAgents(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{
		Targets: []string{"lsegal/glorp"},
		Quotas: map[string]string{
			"claude": "session 95% left, week 94% left",
			"codex":  "weekly 87% left",
			"muse":   "day 40% left",
			"opal":   "",
		},
	}))
	lines := strings.Split(updated.(dashboard).View(), "\n")
	footer := lines[len(lines)-1]
	if width := lipgloss.Width(footer); width > 100 {
		t.Fatalf("status bar width = %d, want it truncated to the terminal's 100", width)
	}
	// Truncation shortens the widest cell rather than dropping cells, so every
	// section is still identifiable.
	for _, want := range []string{"jobs:", "quota:", "targets:"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("status bar %q lost the %q section", footer, want)
		}
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

func TestFormatClaudeQuota(t *testing.T) {
	usage := "Current session: 5% used · resets Aug 16, 2pm (America/Los_Angeles)\n" +
		"Current week (all models): 6% used · resets Aug 19, 7am (America/Los_Angeles)"
	if got := formatClaudeQuota(usage); got != "session 95% left, week 94% left" {
		t.Fatalf("quota = %q", got)
	}
}

func TestFormatClaudeQuotaSessionOnly(t *testing.T) {
	if got := formatClaudeQuota("Current session: 10% used · resets Aug 16, 2pm"); got != "session 90% left" {
		t.Fatalf("quota = %q", got)
	}
}

func TestFormatClaudeQuotaMissingSessionReturnsEmpty(t *testing.T) {
	if got := formatClaudeQuota("no usage data available"); got != "" {
		t.Fatalf("quota = %q, want empty", got)
	}
}

func TestClaudeQuotaRequestIsFreeSlashCommand(t *testing.T) {
	if got := claudeQuotaRequest(); got != "/usage" {
		t.Fatalf("claude quota request = %q, want the local /usage slash command", got)
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
	if maxVisibleJobs != 6 || jobGridColumns != 2 || jobCardHeight != 13 {
		t.Fatalf("agent grid = %d jobs, %d columns, card height %d; want 6, 2, 13", maxVisibleJobs, jobGridColumns, jobCardHeight)
	}
}

func TestJobAgentSummary(t *testing.T) {
	cases := []struct {
		name string
		job  JobSnapshot
		want string
	}{
		{"pending", JobSnapshot{}, "pending"},
		{"agent only", JobSnapshot{Agent: "codex"}, "codex"},
		{"agent and model", JobSnapshot{Agent: "claude", Model: "opus"}, "claude (opus)"},
		{"agent and effort", JobSnapshot{Agent: "codex", Effort: "high"}, "codex (high)"},
		{"agent, model, and effort", JobSnapshot{Agent: "claude", Model: "opus", Effort: "low"}, "claude (opus, low)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jobAgentSummary(c.job); got != c.want {
				t.Errorf("jobAgentSummary(%+v) = %q, want %q", c.job, got, c.want)
			}
		})
	}
}

func TestDashboardShowsAgentModelAndEffort(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	updated, _ = updated.(dashboard).Update(snapshotMsg(GlorpSnapshot{Jobs: []JobSnapshot{{
		Number: 7, Title: "UI", Status: "active", Agent: "claude", Model: "opus", Effort: "low",
	}}}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "agent: claude (opus, low)") {
		t.Errorf("dashboard missing agent summary in %q", view)
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

	cell := renderStatusBar(80, []string{renderJobCounts(GlorpSnapshot{
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

func TestDashboardShowsLastPollTime(t *testing.T) {
	m := newDashboard()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(dashboard)
	updated, _ = m.Update(snapshotMsg(GlorpSnapshot{
		Targets: []string{"o/one"}, Interval: 30 * time.Second,
		LastPoll: time.Date(2026, 8, 30, 14, 5, 9, 0, time.Local),
	}))
	view := updated.(dashboard).View()
	if !strings.Contains(view, "checked 14:05:09") {
		t.Errorf("dashboard missing last poll time in %q", view)
	}
}

func TestDeliveryTextOmitsLastPollBeforeFirstPoll(t *testing.T) {
	text := deliveryText(GlorpSnapshot{Interval: 30 * time.Second})
	if text != "polling every 30s" {
		t.Errorf("delivery text = %q, want no last poll time", text)
	}
}

func TestDeliveryTextReportsLastPollInPushMode(t *testing.T) {
	text := deliveryText(GlorpSnapshot{
		UseWebhooks: true, WebhookOnline: true,
		LastPoll: time.Date(2026, 8, 30, 9, 0, 1, 0, time.Local),
	})
	if text != "push; checked 09:00:01" {
		t.Errorf("delivery text = %q, want push mode last poll time", text)
	}
}

// TestFormatInterval pins the shared interval spelling. The same table is run
// against the web dashboard's formatInterval in webui/src/dashboard.test.js, so
// the two status bars cannot drift apart again (issue #449).
func TestFormatInterval(t *testing.T) {
	cases := []struct {
		interval time.Duration
		want     string
	}{
		{5 * time.Second, "5s"},
		{20 * time.Second, "20s"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{90 * time.Second, "1m30s"},
		{time.Hour, "1h"},
		{time.Hour + 30*time.Minute, "1h30m"},
		{time.Hour + 30*time.Second, "1h30s"},
		{2*time.Hour + 5*time.Minute + 9*time.Second, "2h5m9s"},
		{1500 * time.Millisecond, "1.5s"},
		{500 * time.Millisecond, "500ms"},
		{0, "0s"},
		{-time.Second, "0s"},
	}
	for _, testCase := range cases {
		if got := formatInterval(testCase.interval); got != testCase.want {
			t.Errorf("formatInterval(%v) = %q, want %q", testCase.interval, got, testCase.want)
		}
	}
}

// TestDeliveryTextFormatsWholeHour covers the status bar itself: an hourly poll
// reads as 1h rather than Go's 1h0m0s (issue #449).
func TestDeliveryTextFormatsWholeHour(t *testing.T) {
	if got := deliveryText(GlorpSnapshot{Interval: time.Hour}); got != "polling every 1h" {
		t.Errorf("deliveryText = %q, want %q", got, "polling every 1h")
	}
}

type recordingReporter struct {
	snapshots int
	logs      []string
}

func (r *recordingReporter) Snapshot(GlorpSnapshot) { r.snapshots++ }
func (r *recordingReporter) Log(line string)        { r.logs = append(r.logs, line) }

func TestCombineUIReportersFansOutUpdates(t *testing.T) {
	first, second := &recordingReporter{}, &recordingReporter{}
	reporter := combineUIReporters(nil, first, second)
	reporter.Snapshot(GlorpSnapshot{})
	reporter.Log("ready")
	if first.snapshots != 1 || second.snapshots != 1 || first.logs[0] != "ready" || second.logs[0] != "ready" {
		t.Fatalf("updates were not sent to both reporters: %#v %#v", first, second)
	}
}

func TestCombineUIReportersDropsTypedNilReporters(t *testing.T) {
	var webUI *webui.Server
	terminal := &recordingReporter{}
	reporter := combineUIReporters(terminal, webUI)
	reporter.Snapshot(GlorpSnapshot{})
	reporter.Log("watching o/r")
	if terminal.snapshots != 1 || len(terminal.logs) != 1 {
		t.Fatalf("updates were not sent to the terminal reporter: %#v", terminal)
	}
}

func TestCombineUIReportersReturnsNilWhenOnlyTypedNils(t *testing.T) {
	var webUI *webui.Server
	if reporter := combineUIReporters(nil, webUI); reporter != nil {
		t.Fatalf("combineUIReporters(nil, (*webui.Server)(nil)) = %#v, want nil", reporter)
	}
}
