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
)

const maxVisibleJobs = 6
const jobGridColumns = 2
const jobCardHeight = 12

type JobSnapshot struct {
	Number  int
	Title   string
	Status  string
	Started time.Time
	Log     string
}

type WatchSnapshot struct {
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

type snapshotMsg WatchSnapshot
type logMsg string

type dashboard struct {
	snapshot WatchSnapshot
	viewport viewport.Model
	jobs     map[int]viewport.Model
	spinner  spinner.Model
	logs     []string
	width    int
	height   int
	ready    bool
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
		lipgloss.NewStyle().Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255")).Padding(0, 1),
		lipgloss.NewStyle().Background(lipgloss.Color("54")).Foreground(lipgloss.Color("255")).Padding(0, 1),
		lipgloss.NewStyle().Background(lipgloss.Color("29")).Foreground(lipgloss.Color("255")).Padding(0, 1),
		lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("255")).Padding(0, 1),
	}
)

func newDashboard() dashboard {
	s := spinner.New()
	s.Spinner = spinner.Line
	s.Style = active
	return dashboard{snapshot: WatchSnapshot{}, jobs: make(map[int]viewport.Model), spinner: s}
}

func (m dashboard) Init() tea.Cmd { return spinner.Tick }

func (m dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		logHeight := max(3, msg.Height/3)
		if !m.ready {
			m.viewport = viewport.New(max(1, msg.Width-2), max(1, logHeight-3))
			m.ready = true
		} else {
			m.viewport.Width, m.viewport.Height = max(1, msg.Width-2), max(1, logHeight-3)
		}
		for number, jobViewport := range m.jobs {
			jobViewport.Width = max(1, msg.Width/jobGridColumns-7)
			jobViewport.Height = max(1, jobCardHeight-4)
			m.jobs[number] = jobViewport
		}
	case snapshotMsg:
		m.snapshot = WatchSnapshot(msg)
		slices.SortFunc(m.snapshot.Jobs, func(a, b JobSnapshot) int { return b.Started.Compare(a.Started) })
		if len(m.snapshot.Jobs) > maxVisibleJobs {
			m.snapshot.Jobs = m.snapshot.Jobs[:maxVisibleJobs]
		}
		for _, job := range m.snapshot.Jobs {
			jobViewport, ok := m.jobs[job.Number]
			if !ok {
				jobViewport = viewport.New(max(1, m.width/jobGridColumns-7), max(1, jobCardHeight-4))
			}
			jobViewport.SetContent(job.Log)
			m.jobs[job.Number] = jobViewport
		}
	case logMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 200 {
			m.logs = m.logs[len(m.logs)-200:]
		}
		m.viewport.SetContent(strings.Join(m.logs, "\n"))
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
	}
	return m, nil
}

func (m dashboard) View() string {
	if !m.ready {
		return "Starting gh-watch dashboard..."
	}
	jobs := make([]string, 0, len(m.snapshot.Jobs))
	for _, job := range m.snapshot.Jobs {
		status := job.Status
		style := active
		if status == "complete" {
			style = done
		}
		if status == "failed" {
			style = fail
		}
		jobViewport := m.jobs[job.Number]
		if job.Log == "" {
			jobViewport.SetContent("waiting for output...")
		}
		indicator := " "
		if status == "active" {
			indicator = m.spinner.View()
		}
		jobs = append(jobs, panel.Copy().Padding(0, 1).Width(max(18, m.width/jobGridColumns-4)).Height(jobCardHeight).Render(
			fmt.Sprintf("%s #%d %s\n%s %s", indicator, job.Number, truncate(job.Title, 20), style.Render(status), jobViewport.View())))
	}
	rows := make([]string, 0, (len(jobs)+jobGridColumns-1)/jobGridColumns)
	for i := 0; i < len(jobs); i += jobGridColumns {
		end := min(i+jobGridColumns, len(jobs))
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, jobs[i:end]...))
	}
	grid := lipgloss.JoinVertical(lipgloss.Left, rows...)
	logHeight := max(3, m.height/3)
	logs := logPanel.Copy().Width(max(1, m.width-2)).Height(max(1, logHeight-2)).Render(muted.Render("Logs") + "\n" + m.viewport.View())
	counts := fmt.Sprintf("jobs: %s idle %s active %s total", muted.Render(fmt.Sprint(max(0, m.snapshot.Concurrency-m.snapshot.Running-m.snapshot.Queued))), active.Render(fmt.Sprint(m.snapshot.Running+m.snapshot.Queued)), fmt.Sprint(m.snapshot.Completed+m.snapshot.Failed+m.snapshot.Running+m.snapshot.Queued))
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
	return lipgloss.JoinVertical(lipgloss.Left, grid, logs, footer)
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
	ui.program = tea.NewProgram(newDashboard(), tea.WithAltScreen())
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
func (ui *TerminalUI) Snapshot(snapshot WatchSnapshot) {
	ui.program.Send(snapshotMsg(snapshot))
}
func (ui *TerminalUI) Log(line string) { ui.program.Send(logMsg(line)) }

func truncate(s string, width int) string {
	if width < 1 {
		return ""
	}
	if len([]rune(s)) <= width {
		return s
	}
	return string([]rune(s)[:max(0, width-1)]) + "..."
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
