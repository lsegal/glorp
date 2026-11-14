package main

import (
	"fmt"
	"strings"

	"github.com/lsegal/glorp/core"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// promptKey opens the terminal dashboard's prompt area (issue #801).
const promptKey = "p"

// promptsMsg carries the prompt area's conversation into the dashboard.
type promptsMsg []core.PromptEntry

// promptArea is the terminal dashboard's prompt area: whether it is open,
// what is typed so far, the conversation, and its scroll position.
type promptArea struct {
	open    bool
	input   []rune
	entries []core.PromptEntry
	view    viewport.Model
}

// SetPromptHandler wires the prompt area (issue #801) to a function that
// starts answering a request and returns at once, the same one the web
// dashboard's prompt area uses.
func (ui *TerminalUI) SetPromptHandler(handler func(string) (core.PromptEntry, error)) {
	ui.mu.Lock()
	ui.prompt = handler
	ui.mu.Unlock()
}

// Prompts shows the prompt area's conversation.
func (ui *TerminalUI) Prompts(entries []core.PromptEntry) { ui.program.Send(promptsMsg(entries)) }

// submitPrompt hands a request to the prompt agent off the dashboard's
// goroutine and logs a request it refused; the answer arrives as a promptsMsg.
func (ui *TerminalUI) submitPrompt(request string) tea.Cmd {
	ui.mu.Lock()
	handler := ui.prompt
	ui.mu.Unlock()
	if handler == nil {
		return func() tea.Msg { return logMsg("the prompt agent is not available yet") }
	}
	return func() tea.Msg {
		if _, err := handler(request); err != nil {
			return logMsg(fmt.Sprintf("prompt request failed: %v", err))
		}
		return nil
	}
}

// updatePrompts records a new conversation, and, while the prompt area is
// closed, logs each answer that just arrived so it is not missed.
func (m dashboard) updatePrompts(entries []core.PromptEntry) (tea.Model, tea.Cmd) {
	pending := map[int]bool{}
	for _, entry := range m.prompt.entries {
		if entry.Status == "pending" {
			pending[entry.ID] = true
		}
	}
	m.prompt.entries = entries
	m.refreshPromptView()
	if m.prompt.open {
		return m, nil
	}
	var model tea.Model = m
	for _, entry := range entries {
		if pending[entry.ID] && entry.Status != "pending" {
			model, _ = model.(dashboard).Update(logMsg(fmt.Sprintf("prompt agent answered %q (%s); press %s to read it", truncate(entry.Request, 60), entry.Status, promptKey)))
		}
	}
	return model, nil
}

// updatePromptKey handles a key press while the prompt area is open: typing
// edits the request, Enter sends it, Esc closes the area, and the arrow and
// page keys scroll the conversation.
func (m dashboard) updatePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.prompt.open = false
		return m, nil
	case tea.KeyEnter:
		request := strings.TrimSpace(string(m.prompt.input))
		if request == "" || m.ui == nil {
			return m, nil
		}
		m.prompt.input = nil
		return m, m.ui.submitPrompt(request)
	case tea.KeyBackspace:
		if len(m.prompt.input) > 0 {
			m.prompt.input = m.prompt.input[:len(m.prompt.input)-1]
		}
		return m, nil
	case tea.KeyCtrlU:
		m.prompt.input = nil
		return m, nil
	case tea.KeySpace:
		m.prompt.input = append(m.prompt.input, ' ')
		return m, nil
	case tea.KeyRunes:
		if !msg.Paste || !strings.ContainsAny(string(msg.Runes), "\r\n") {
			m.prompt.input = append(m.prompt.input, msg.Runes...)
			return m, nil
		}
		m.prompt.input = append(m.prompt.input, []rune(strings.Join(strings.Fields(string(msg.Runes)), " "))...)
		return m, nil
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
		var cmd tea.Cmd
		m.prompt.view, cmd = m.prompt.view.Update(msg)
		return m, cmd
	}
	return m, nil
}

// updatePromptMouse scrolls the conversation with the wheel and ignores every
// other mouse event, so nothing under the prompt area is clicked.
func (m dashboard) updatePromptMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if tea.MouseEvent(msg).IsWheel() {
		var cmd tea.Cmd
		m.prompt.view, cmd = m.prompt.view.Update(msg)
		return m, cmd
	}
	return m, nil
}

// openPrompt opens the prompt area scrolled to the latest exchange.
func (m dashboard) openPrompt() (tea.Model, tea.Cmd) {
	m.prompt.open = true
	m.refreshPromptView()
	m.prompt.view.GotoBottom()
	return m, nil
}

// promptViewSize is the conversation viewport's size: the whole screen less
// the panel border, its title, and the input line.
func (m dashboard) promptViewSize() (int, int) {
	return max(1, m.width-6), max(1, m.height-6)
}

// refreshPromptView sizes the conversation to the screen and fills it,
// following the latest exchange unless it was scrolled up.
func (m *dashboard) refreshPromptView() {
	width, height := m.promptViewSize()
	follow := m.prompt.view.Height == 0 || m.prompt.view.AtBottom()
	if m.prompt.view.Height == 0 {
		m.prompt.view = viewport.New(width, height)
	}
	m.prompt.view.Width, m.prompt.view.Height = width, height
	m.prompt.view.SetContent(renderPromptEntries(m.prompt.entries, width))
	if follow {
		m.prompt.view.GotoBottom()
	}
}

// renderPromptEntries draws the conversation, each request followed by the
// agent's answer and the outcome of every action it asked for.
func renderPromptEntries(entries []core.PromptEntry, width int) string {
	if len(entries) == 0 {
		return muted.Render("Ask about this glorp instance and its jobs, or ask it to retry, pause, resume, or stop a job, run an unblock round, update an issue or pull request, or file an issue.")
	}
	wrap := lipgloss.NewStyle().Width(max(1, width))
	var blocks []string
	for _, entry := range entries {
		block := []string{barStyle.Render("you: ") + wrap.Width(max(1, width-5)).Render(entry.Request)}
		agent := "agent"
		if entry.Agent != "" {
			agent += " (" + entry.Agent + ")"
		}
		switch entry.Status {
		case "pending":
			block = append(block, muted.Render(agent+": working on it, started "+entry.Started.Format("15:04:05")+"..."))
		case "failed":
			block = append(block, fail.Render(agent+" failed:"), wrap.Render(entry.Response))
		default:
			block = append(block, active.Render(agent+":"), wrap.Render(entry.Response))
		}
		for _, action := range entry.Actions {
			block = append(block, done.Render("-> ")+wrap.Width(max(1, width-3)).Render(action))
		}
		blocks = append(blocks, strings.Join(block, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

// promptView draws the open prompt area over the whole dashboard.
func (m dashboard) promptView() string {
	width, _ := m.promptViewSize()
	title := barStyle.Render("Prompt agent") + muted.Render(" - this glorp instance and its jobs only. Enter sends, Esc closes, ↑/↓ scroll.")
	input := string(m.prompt.input)
	if limit := width - 3; len([]rune(input)) > limit && limit > 0 {
		runes := []rune(input)
		input = string(runes[len(runes)-limit:])
	}
	line := barStyle.Render("> ") + input + "█"
	body := strings.Join([]string{title, renderViewport(m.prompt.view), line}, "\n")
	return panel.Copy().Padding(0, 1).Width(max(1, m.width-2)).Height(max(1, m.height-2)).Render(body)
}
