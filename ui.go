package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const maxVisibleJobs = 6
const jobGridColumns = 2
const jobCardHeight = 12
const dashboardGap = 1

type JobSnapshot struct {
	Number            int
	Title             string
	Status            string
	CheckoutDirectory string
	SessionID         string
	Started           time.Time
	Log               string
}

type GlorpSnapshot struct {
	Targets       []string
	IssueCounts   map[string]int
	Running       int
	Queued        int
	Completed     int
	Failed        int
	Concurrency   int
	NextPoll      time.Time
	Interval      time.Duration
	UseWebhooks   bool
	WebhookURL    string
	WebhookOnline bool
	TokensUsed    int
	TokenLimit    int
	Quota         string
	Jobs          []JobSnapshot
}

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
			jobViewport.Height = max(1, jobCardHeight-4)
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
		slices.SortFunc(m.snapshot.Jobs, func(a, b JobSnapshot) int { return b.Started.Compare(a.Started) })
		if len(m.snapshot.Jobs) > maxVisibleJobs {
			m.snapshot.Jobs = m.snapshot.Jobs[:maxVisibleJobs]
		}
		for _, job := range m.snapshot.Jobs {
			jobViewport, ok := m.jobs[job.Number]
			if !ok {
				jobViewport = viewport.New(max(1, m.width/jobGridColumns-7), max(1, jobCardHeight-4))
			}
			followOutput := !ok || jobViewport.AtBottom()
			jobViewport.SetContent(job.Log)
			if followOutput {
				jobViewport.GotoBottom()
			}
			m.jobs[job.Number] = jobViewport
		}
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
	regions := make([]viewportRegion, 0, len(m.snapshot.Jobs)+1)
	cardRenderWidth := lipgloss.Width(panel.Copy().Padding(0, 1).Width(jobCardWidth(m.width)).Height(jobCardHeight).Render(""))
	for i, job := range m.snapshot.Jobs {
		view, ok := m.jobs[job.Number]
		if !ok {
			continue
		}
		x := (i%jobGridColumns)*(cardRenderWidth+dashboardGap) + 1
		y := (i/jobGridColumns)*(jobCardHeight+dashboardGap) + 3
		regions = append(regions, viewportRegion{
			target: viewportTarget{jobNumber: job.Number}, x: x, y: y,
			width: view.Width + 1, height: view.Height, contentEnd: x + view.Width,
		})
	}
	gridRows := (len(m.snapshot.Jobs) + jobGridColumns - 1) / jobGridColumns
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
	jobs := make([]string, 0, len(m.snapshot.Jobs))
	for _, job := range m.snapshot.Jobs {
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
		jobs = append(jobs, panel.Copy().Padding(0, 1).Width(cardWidth).Height(jobCardHeight).Render(
			fmt.Sprintf("%s\n%s\n%s\n%s", title, checkout, session, progress)))
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
	push := "polling every " + m.snapshot.Interval.String()
	if m.snapshot.UseWebhooks {
		push = "push"
		if m.snapshot.WebhookURL != "" {
			push += " " + m.snapshot.WebhookURL
		}
		if !m.snapshot.WebhookOnline {
			push += " (offline)"
		}
	}
	targets := "targets: " + strings.Join(formatTargets(m.snapshot.Targets, m.snapshot.IssueCounts), ", ")
	footer := renderStatusBar([]string{counts, tokens, push, targets})
	sections := []string{logs, footer}
	if grid != "" {
		sections = append([]string{grid}, sections...)
	}
	return joinVerticalWithGap(sections, dashboardGap)
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

func renderStatusBar(items []string) string {
	cells := make([]string, len(items))
	for i, item := range items {
		cells[i] = statusBars[i%len(statusBars)].Render(item)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
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
