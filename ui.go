package main

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/lsegal/glorp/core"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const maxVisibleJobs = 6
const jobGridColumns = 2
const jobCardHeight = 13
const dashboardGap = 1

// jobStatusRank orders jobs so the "last 6" viewports (issue #587) show the
// jobs an operator most needs to see rather than whichever six happened to
// start most recently: running work first, then errored, then anything else
// still in flight, with completed work pushed to the bottom.
func jobStatusRank(status string) int {
	switch status {
	case "active":
		return 0
	case "failed":
		return 1
	case "complete":
		return 3
	default:
		return 2
	}
}

// sortJobSnapshots orders jobs by jobStatusRank, breaking ties within a rank
// by most recently started first.
func sortJobSnapshots(jobs []JobSnapshot) {
	slices.SortFunc(jobs, func(a, b JobSnapshot) int {
		if rankDiff := jobStatusRank(a.Status) - jobStatusRank(b.Status); rankDiff != 0 {
			return rankDiff
		}
		return b.Started.Compare(a.Started)
	})
}

// JobSnapshot and GlorpSnapshot live in package core so the browser dashboard
// in package webui can render the same published state as this one.
type JobSnapshot = core.JobSnapshot

type GlorpSnapshot = core.Snapshot

type snapshotMsg GlorpSnapshot
type logMsg string

type viewportTarget struct {
	jobNumber int
	logs      bool
}

type viewportRegion struct {
	target     viewportTarget
	x, y       int
	width      int
	height     int
	contentEnd int
}

type dashboard struct {
	snapshot GlorpSnapshot
	viewport viewport.Model
	jobs     map[int]viewport.Model
	spinner  spinner.Model
	logs     []string
	width    int
	height   int
	ready    bool
	page     int
	dragging *viewportTarget
}

var (
	barStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	muted      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	active     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	done       = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	fail       = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	panel      = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	logPanel   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252"))
	statusBars = []lipgloss.Style{
		lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255")),
		lipgloss.NewStyle().Background(lipgloss.Color("54")).Foreground(lipgloss.Color("255")).Padding(0, 1),
		lipgloss.NewStyle().Background(lipgloss.Color("29")).Foreground(lipgloss.Color("255")).Padding(0, 1),
		lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("255")).Padding(0, 1),
	}
	countLabelStyle  = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255"))
	idleCountStyle   = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("241"))
	activeCountStyle = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("42"))
	totalCountStyle  = lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("205"))
)

func newDashboard() dashboard {
	s := spinner.New()
	s.Spinner = spinner.Line
	s.Style = active
	return dashboard{snapshot: GlorpSnapshot{}, jobs: make(map[int]viewport.Model), spinner: s}
}

func (m dashboard) Init() tea.Cmd { return spinner.Tick }

func (m dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		followLogs := !m.ready || m.viewport.AtBottom()
		followJobs := make(map[int]bool, len(m.jobs))
		for number, jobViewport := range m.jobs {
			followJobs[number] = jobViewport.AtBottom()
		}
		m.width, m.height = msg.Width, msg.Height
		logHeight := max(3, msg.Height/3)
		if !m.ready {
			m.viewport = viewport.New(max(1, msg.Width-3), max(1, logHeight-3))
			m.ready = true
		} else {
			m.viewport.Width, m.viewport.Height = max(1, msg.Width-3), max(1, logHeight-3)
		}
		for number, jobViewport := range m.jobs {
			jobViewport.Width = max(1, msg.Width/jobGridColumns-7)
			jobViewport.Height = max(1, jobCardHeight-5)
			if followJobs[number] {
				jobViewport.GotoBottom()
			}
			m.jobs[number] = jobViewport
		}
		if followLogs {
			m.viewport.GotoBottom()
		}
	case snapshotMsg:
		m.snapshot = GlorpSnapshot(msg)
		sortJobSnapshots(m.snapshot.Jobs)
		if len(m.snapshot.Jobs) > maxVisibleJobs {
			m.snapshot.Jobs = m.snapshot.Jobs[:maxVisibleJobs]
		}
		for _, job := range m.snapshot.Jobs {
			jobViewport, ok := m.jobs[job.Number]
			if !ok {
				jobViewport = viewport.New(max(1, m.width/jobGridColumns-7), max(1, jobCardHeight-5))
			}
			followOutput := !ok || jobViewport.AtBottom()
			jobViewport.SetContent(job.Log)
			if followOutput {
				jobViewport.GotoBottom()
			}
			m.jobs[job.Number] = jobViewport
		}
		_, m.page, _ = m.visibleJobs()
	case logMsg:
		followOutput := m.viewport.AtBottom()
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 200 {
			m.logs = m.logs[len(m.logs)-200:]
		}
		m.viewport.SetContent(strings.Join(m.logs, "\n"))
		if followOutput {
			m.viewport.GotoBottom()
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if _, page, pages := m.visibleJobs(); pages > 1 {
			switch msg.String() {
			case "left", "h":
				m.page = (page + pages - 1) % pages
				return m, nil
			case "right", "l":
				m.page = (page + 1) % pages
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		for number, jobViewport := range m.jobs {
			var jobCmd tea.Cmd
			m.jobs[number], jobCmd = jobViewport.Update(msg)
			if jobCmd != nil {
				cmd = tea.Batch(cmd, jobCmd)
			}
		}
		return m, cmd
	case tea.MouseMsg:
		return m.updateMouse(msg)
	}
	return m, nil
}

func (m dashboard) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	event := tea.MouseEvent(msg)
	if event.Action == tea.MouseActionRelease {
		m.dragging = nil
		return m, nil
	}
	if m.dragging != nil && event.Action == tea.MouseActionMotion {
		m.scrollToMouse(*m.dragging, event.Y)
		return m, nil
	}
	region, ok := m.viewportAt(event.X, event.Y)
	if !ok {
		return m, nil
	}
	view := m.viewportFor(region.target)
	if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
		if event.X == region.contentEnd {
			target := region.target
			m.dragging = &target
			m.scrollToMouse(target, event.Y)
			return m, nil
		}
		if !view.AtBottom() && event.Y == region.y+region.height-1 && event.X >= region.contentEnd-moreIndicatorWidth {
			view.GotoBottom()
			m.setViewport(region.target, view)
		}
		return m, nil
	}
	if event.IsWheel() {
		updated, cmd := view.Update(msg)
		m.setViewport(region.target, updated)
		return m, cmd
	}
	return m, nil
}

func (m *dashboard) scrollToMouse(target viewportTarget, mouseY int) {
	region, ok := m.regionFor(target)
	if !ok {
		return
	}
	view := m.viewportFor(target)
	maxOffset := max(0, view.TotalLineCount()-view.Height)
	row := min(max(mouseY-region.y, 0), max(0, region.height-1))
	if region.height > 1 {
		view.SetYOffset((row*maxOffset + (region.height-1)/2) / (region.height - 1))
	} else {
		view.SetYOffset(maxOffset)
	}
	m.setViewport(target, view)
}

func (m dashboard) viewportFor(target viewportTarget) viewport.Model {
	if target.logs {
		return m.viewport
	}
	return m.jobs[target.jobNumber]
}

func (m *dashboard) setViewport(target viewportTarget, view viewport.Model) {
	if target.logs {
		m.viewport = view
		return
	}
	m.jobs[target.jobNumber] = view
}

func (m dashboard) viewportAt(x, y int) (viewportRegion, bool) {
	for _, region := range m.viewportRegions() {
		if x >= region.x && x < region.x+region.width && y >= region.y && y < region.y+region.height {
			return region, true
		}
	}
	return viewportRegion{}, false
}

func (m dashboard) regionFor(target viewportTarget) (viewportRegion, bool) {
	for _, region := range m.viewportRegions() {
		if region.target == target {
			return region, true
		}
	}
	return viewportRegion{}, false
}

func (m dashboard) viewportRegions() []viewportRegion {
	visible, _, _ := m.visibleJobs()
	regions := make([]viewportRegion, 0, len(visible)+1)
	cardRenderWidth := lipgloss.Width(panel.Copy().Padding(0, 1).Width(jobCardWidth(m.width)).Height(jobCardHeight).Render(""))
	for i, job := range visible {
		view, ok := m.jobs[job.Number]
		if !ok {
			continue
		}
		x := (i%jobGridColumns)*(cardRenderWidth+dashboardGap) + 1
		y := (i/jobGridColumns)*(jobCardHeight+dashboardGap) + 4
		regions = append(regions, viewportRegion{
			target: viewportTarget{jobNumber: job.Number}, x: x, y: y,
			width: view.Width + 1, height: view.Height, contentEnd: x + view.Width,
		})
	}
	gridRows := (len(visible) + jobGridColumns - 1) / jobGridColumns
	logsY := 1
	if gridRows > 0 {
		logsY = gridRows*jobCardHeight + (gridRows-1)*dashboardGap + dashboardGap + 1
	}
	regions = append(regions, viewportRegion{
		target: viewportTarget{logs: true}, x: 0, y: logsY,
		width: m.viewport.Width + 1, height: m.viewport.Height, contentEnd: m.viewport.Width,
	})
	return regions
}

func (m dashboard) View() string {
	if !m.ready {
		return "Starting glorp dashboard..."
	}
	visible, page, pages := m.visibleJobs()
	jobs := make([]string, 0, len(visible))
	for _, job := range visible {
		status := job.Status
		jobViewport := m.jobs[job.Number]
		if job.Log == "" {
			jobViewport.SetContent("waiting for output...")
		}
		progress := renderViewport(jobViewport)
		indicator := " "
		if status == "active" {
			indicator = m.spinner.View()
		}
		if status == "complete" {
			indicator = done.Render("✓")
		}
		cardWidth := jobCardWidth(m.width)
		prefix := fmt.Sprintf("%s #%d ", indicator, job.Number)
		title := panel.Copy().Width(max(1, cardWidth-2)).Render(prefix + truncate(job.Title, jobTitleWidth(cardWidth, prefix)))
		metadataWidth := max(1, cardWidth-2)
		checkout := muted.Render(truncate("checkout: "+job.CheckoutDirectory, metadataWidth))
		session := muted.Render(truncate("session: "+job.SessionID, metadataWidth))
		agent := muted.Render(truncate("agent: "+jobAgentSummary(job), metadataWidth))
		jobs = append(jobs, panel.Copy().Padding(0, 1).Width(cardWidth).Height(jobCardHeight).Render(
			fmt.Sprintf("%s\n%s\n%s\n%s\n%s", title, checkout, session, agent, progress)))
	}
	rows := make([]string, 0, (len(jobs)+jobGridColumns-1)/jobGridColumns)
	for i := 0; i < len(jobs); i += jobGridColumns {
		end := min(i+jobGridColumns, len(jobs))
		rows = append(rows, joinHorizontalWithGap(jobs[i:end], dashboardGap))
	}
	grid := joinVerticalWithGap(rows, dashboardGap)
	logHeight := max(3, m.height/3)
	logs := logPanel.Copy().Width(max(1, m.width-2)).Height(max(1, logHeight-2)).Render(muted.Render("Logs") + "\n" + renderViewport(m.viewport))
	counts := renderJobCounts(m.snapshot)
	tokens := quotaText(m.snapshot)
	push := deliveryText(m.snapshot)
	targets := "targets: " + strings.Join(formatTargets(m.snapshot.Targets, m.snapshot.IssueCounts), ", ")
	items := []string{counts, tokens, push, targets}
	if m.snapshot.Identity != "" {
		items = append([]string{"id: " + m.snapshot.Identity}, items...)
	}
	footer := renderStatusBar(m.width, items)
	sections := []string{logs, footer}
	if pages > 1 {
		sections = append(sections, muted.Render(fmt.Sprintf(pagerHint, page+1, pages)))
	}
	if m.snapshot.WebUIURL != "" {
		sections = append(sections, muted.Render("web dashboard: "+m.snapshot.WebUIURL))
	}
	if grid != "" {
		sections = append([]string{grid}, sections...)
	}
	return joinVerticalWithGap(sections, dashboardGap)
}

// pagerHint tells the operator that agent cards continue on another page and
// which keys move between them. Without it a paged grid is indistinguishable
// from a truncated one (issue #617).
const pagerHint = "page %d/%d  ←/→ (or h/l) for more agents"

// jobsPerPage reports how many agent cards fit above the log panel at this
// terminal height. The grid used to render a fixed three rows of cards, so on
// a shorter terminal the top row was pushed off screen entirely and those
// viewports could neither be read nor scrolled (issue #617).
//
// extraLines counts the persistent bottom lines rendered under the status bar
// (the web dashboard URL, the pager hint), each of which also costs the gap
// line joinVerticalWithGap puts above it.
func jobsPerPage(height, extraLines int) int {
	logHeight := max(3, height/3)
	// The grid, the log panel, the status bar, and each extra line are joined
	// with one blank gap line between them.
	chrome := max(1, logHeight-2) + 1 + extraLines
	available := height - chrome - (2 + extraLines)
	rows := (available + dashboardGap) / (jobCardHeight + dashboardGap)
	return max(1, rows) * jobGridColumns
}

// visibleJobs returns the agent cards belonging to the current page, the page
// index clamped to the pages that exist, and the total number of pages.
func (m dashboard) visibleJobs() ([]JobSnapshot, int, int) {
	extraLines := 0
	if m.snapshot.WebUIURL != "" {
		extraLines++
	}
	perPage := jobsPerPage(m.height, extraLines)
	if len(m.snapshot.Jobs) > perPage {
		// Paging costs one more bottom line, which can cost a whole card row.
		perPage = jobsPerPage(m.height, extraLines+1)
	}
	if len(m.snapshot.Jobs) == 0 {
		return nil, 0, 0
	}
	pages := (len(m.snapshot.Jobs) + perPage - 1) / perPage
	page := min(max(m.page, 0), pages-1)
	start := page * perPage
	return m.snapshot.Jobs[start:min(start+perPage, len(m.snapshot.Jobs))], page, pages
}

const moreIndicator = "more ↓"

var moreIndicatorWidth = lipgloss.Width(moreIndicator)

func renderViewport(view viewport.Model) string {
	content := view.View()
	if !view.AtBottom() {
		lines := strings.Split(content, "\n")
		last := len(lines) - 1
		prefixWidth := max(0, view.Width-moreIndicatorWidth)
		lines[last] = ansi.Truncate(lines[last], prefixWidth, "") + active.Render(moreIndicator)
		content = strings.Join(lines, "\n")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, content, renderScrollbar(view))
}

func renderScrollbar(view viewport.Model) string {
	height := max(1, view.Height)
	total := max(1, view.TotalLineCount())
	thumbHeight := height
	if total > height {
		thumbHeight = max(1, height*height/total)
	}
	thumbTop := 0
	if travel := height - thumbHeight; travel > 0 {
		thumbTop = int(view.ScrollPercent()*float64(travel) + 0.5)
	}
	lines := make([]string, height)
	for i := range lines {
		lines[i] = muted.Render("│")
		if i >= thumbTop && i < thumbTop+thumbHeight {
			lines[i] = active.Render("█")
		}
	}
	return strings.Join(lines, "\n")
}

// formatInterval renders a poll interval for display. Go's own duration string
// spells whole hours as `1h0m0s`, and the web UI used to divide the same
// nanosecond value into whole seconds (`3600s`), so one run read differently
// depending on which status bar was open (issue #449). Zero components are
// dropped so short intervals stay as short as they read (`20s`, not `0h0m20s`),
// and the web dashboard's formatInterval in webui/src/dashboard.js mirrors this
// exactly; the two are covered by the same table of cases on each side.
func formatInterval(d time.Duration) string { return core.FormatInterval(d) }

// deliveryText describes how the run is picking work up, and when it last
// checked GitHub. The last-checked time is the only sign a quiet poll loop is
// still running, since a poll that finds nothing new no longer logs anything
// (issue #447); it is shown for push mode too, which still reconciles on a
// periodic poll. A run that has not completed a poll yet reports no time
// rather than the zero clock.
func deliveryText(snapshot GlorpSnapshot) string {
	text := "polling every " + formatInterval(snapshot.Interval)
	if snapshot.UseWebhooks {
		text = "push"
		if snapshot.WebhookURL != "" {
			text += " " + snapshot.WebhookURL
		}
		if !snapshot.WebhookOnline {
			text += " (offline)"
		}
	}
	if !snapshot.LastPoll.IsZero() {
		text += "; checked " + snapshot.LastPoll.Format("15:04:05")
	}
	return text
}

func renderJobCounts(snapshot GlorpSnapshot) string {
	idle := max(0, snapshot.Concurrency-snapshot.Running-snapshot.Queued)
	activeJobs := snapshot.Running + snapshot.Queued
	total := snapshot.Completed + snapshot.Failed + activeJobs
	// Render every visible character with the cell background. A nested
	// Lipgloss span resets its parent style when it ends, so relying on an
	// outer background leaves the labels between colored counts unpainted.
	return fmt.Sprintf("%s%s%s%s%s%s%s",
		countLabelStyle.Render(" jobs: "),
		idleCountStyle.Render(fmt.Sprint(idle)),
		countLabelStyle.Render(" idle "),
		activeCountStyle.Render(fmt.Sprint(activeJobs)),
		countLabelStyle.Render(" active "),
		totalCountStyle.Render(fmt.Sprint(total)),
		countLabelStyle.Render(" total "),
	)
}

func joinHorizontalWithGap(items []string, gap int) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items)*2-1)
	for i, item := range items {
		if i > 0 {
			parts = append(parts, strings.Repeat(" ", max(0, gap)))
		}
		parts = append(parts, item)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func joinVerticalWithGap(items []string, gap int) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items)*2-1)
	for i, item := range items {
		if i > 0 {
			for j := 0; j < max(0, gap); j++ {
				parts = append(parts, "")
			}
		}
		parts = append(parts, item)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func renderStatusBar(width int, items []string) string {
	rows := wrapStatusBarItems(items, width)
	lines := make([]string, 0, len(rows))
	index := 0
	for _, row := range rows {
		row = fitStatusBarItems(row, width, index)
		cells := make([]string, len(row))
		for i, item := range row {
			cells[i] = statusBars[(index+i)%len(statusBars)].Render(item)
		}
		index += len(row)
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// statusBarCellPadding is the horizontal padding statusBars adds to every cell
// but the first, which fitStatusBarItems has to count as spent width.
const statusBarCellPadding = 2

// minStatusBarCellWidth is the narrowest a status bar cell may be truncated to
// before the bar stops squeezing and wraps onto another line (issue #616).
// Under ten columns a section is shortened past the point of being readable.
const minStatusBarCellWidth = 10

// statusBarCellOverhead reports the columns the style at the given position in
// the bar spends on padding, so the first cell of the bar costs nothing extra.
func statusBarCellOverhead(index int) int {
	if index%len(statusBars) == 0 {
		return 0
	}
	return statusBarCellPadding
}

// wrapStatusBarItems groups the bar's sections into rows that each have room
// to show every one of their sections at minStatusBarCellWidth. Rather than
// truncate a section into an unreadable stub, the bar rolls onto another line
// (issue #616). A row always keeps at least one section, so a terminal too
// narrow for even one still gets a truncated cell instead of an empty bar.
func wrapStatusBarItems(items []string, width int) [][]string {
	if len(items) == 0 {
		return nil
	}
	if width < 1 {
		return [][]string{items}
	}
	var rows [][]string
	var row []string
	index, spent := 0, 0
	for _, item := range items {
		need := min(lipgloss.Width(item), minStatusBarCellWidth) + statusBarCellOverhead(index)
		if len(row) > 0 && spent+need > width {
			rows = append(rows, row)
			row, spent = nil, 0
			need = min(lipgloss.Width(item), minStatusBarCellWidth) + statusBarCellOverhead(index)
		}
		row = append(row, item)
		spent += need
		index++
	}
	return append(rows, row)
}

// fitStatusBarItems shrinks one row's cells until it fits the terminal. The
// quota cell grows with every agent a run is configured with (issue #489), so
// a four-agent run would otherwise push the bar past the terminal width and
// wrap it mid-cell, shoving the job grid up. The widest cell is truncated
// first and repeatedly, so a long quota list gives way before the counts and
// the targets beside it, and no cell is squeezed below
// minStatusBarCellWidth. offset is the row's first cell's position in the
// whole bar, which decides how much padding each cell spends.
func fitStatusBarItems(items []string, width, offset int) []string {
	if width < 1 || len(items) == 0 {
		return items
	}
	fitted := append([]string(nil), items...)
	spent := func() int {
		total := 0
		for i, item := range fitted {
			total += lipgloss.Width(item) + statusBarCellOverhead(offset+i)
		}
		return total
	}
	for spent() > width {
		widest := -1
		for i, item := range fitted {
			if lipgloss.Width(item) <= minStatusBarCellWidth {
				continue
			}
			if widest < 0 || lipgloss.Width(item) > lipgloss.Width(fitted[widest]) {
				widest = i
			}
		}
		if widest < 0 {
			// Every cell already sits at the minimum useful width, which only
			// leaves the bar over budget on a terminal too narrow for a single
			// section. Truncate that lone cell rather than overflow.
			if len(fitted) == 1 {
				fitted[0] = truncate(fitted[0], max(1, width-statusBarCellOverhead(offset)))
			}
			break
		}
		room := max(minStatusBarCellWidth, lipgloss.Width(fitted[widest])-(spent()-width))
		shrunk := truncate(fitted[widest], room)
		if shrunk == fitted[widest] {
			break
		}
		fitted[widest] = shrunk
	}
	return fitted
}

type dashboardWriter struct{ ui *TerminalUI }

func (w dashboardWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.ui.program.Send(logMsg(line))
		}
	}
	return len(p), nil
}

type TerminalUI struct {
	program *tea.Program
}

func NewTerminalUI() *TerminalUI {
	ui := &TerminalUI{}
	ui.program = tea.NewProgram(newDashboard(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	return ui
}
func (ui *TerminalUI) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			ui.program.Quit()
		case <-done:
		}
	}()
	_, err := ui.program.Run()
	close(done)
	return err
}
func (ui *TerminalUI) Writer() io.Writer { return dashboardWriter{ui: ui} }
func (ui *TerminalUI) Snapshot(snapshot GlorpSnapshot) {
	ui.program.Send(snapshotMsg(snapshot))
}
func (ui *TerminalUI) Log(line string) { ui.program.Send(logMsg(line)) }

func jobAgentSummary(job JobSnapshot) string {
	if job.Agent == "" {
		return "pending"
	}
	summary := job.Agent
	if job.Model != "" {
		summary += " (" + job.Model
		if job.Effort != "" {
			summary += ", " + job.Effort
		}
		summary += ")"
	} else if job.Effort != "" {
		summary += " (" + job.Effort + ")"
	}
	return summary
}

func truncate(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 3 {
		var result []rune
		for _, r := range s {
			candidate := string(append(result, r))
			if lipgloss.Width(candidate) > width {
				break
			}
			result = append(result, r)
		}
		return string(result)
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes[:len(runes)]))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func jobCardWidth(width int) int {
	return max(18, width/jobGridColumns-4)
}

func jobTitleWidth(cardWidth int, prefix string) int {
	return max(1, cardWidth-2-lipgloss.Width(prefix))
}
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatTargets(targets []string, counts map[string]int) []string {
	result := make([]string, len(targets))
	for i, target := range targets {
		result[i] = fmt.Sprintf("%s (%d issues)", target, counts[target])
	}
	return result
}

type multiUIReporter []UIReporter

func (reporters multiUIReporter) Snapshot(snapshot GlorpSnapshot) {
	for _, reporter := range reporters {
		if !isNilUIReporter(reporter) {
			reporter.Snapshot(snapshot)
		}
	}
}

func (reporters multiUIReporter) Log(line string) {
	for _, reporter := range reporters {
		if !isNilUIReporter(reporter) {
			reporter.Log(line)
		}
	}
}

// isNilUIReporter reports whether a reporter is unusable, including a typed-nil
// pointer such as a (*webui.Server)(nil) stored in the UIReporter interface.
func isNilUIReporter(reporter UIReporter) bool {
	if reporter == nil {
		return true
	}
	switch value := reflect.ValueOf(reporter); value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return value.IsNil()
	default:
		return false
	}
}

func combineUIReporters(reporters ...UIReporter) UIReporter {
	combined := make(multiUIReporter, 0, len(reporters))
	for _, reporter := range reporters {
		if !isNilUIReporter(reporter) {
			combined = append(combined, reporter)
		}
	}
	if len(combined) == 0 {
		return nil
	}
	return combined
}
