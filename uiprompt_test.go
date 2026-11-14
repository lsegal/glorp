package main

import (
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// typeText sends text to the dashboard one key at a time.
func typeText(m dashboard, text string) dashboard {
	for _, r := range text {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			key = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		updated, _ := m.Update(key)
		m = updated.(dashboard)
	}
	return m
}

// Pressing "p" opens the prompt area, where every key types into the
// request, "q" included, Enter sends it without waiting for the answer, and
// Esc closes the area (issue #801).
func TestDashboardPromptAreaSendsRequests(t *testing.T) {
	ui := &TerminalUI{}
	sent := make(chan string, 1)
	ui.SetPromptHandler(func(request string) (core.PromptEntry, error) {
		sent <- request
		return core.PromptEntry{ID: 1, Request: request, Status: "pending"}, nil
	})
	m := newDashboard(ui)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(dashboard)
	updated, _ = m.Update(runeKey('p'))
	m = updated.(dashboard)
	if !m.prompt.open || !strings.Contains(ansi.Strip(m.View()), "Prompt agent") {
		t.Fatalf("p did not open the prompt area:\n%s", ansi.Strip(m.View()))
	}
	m = typeText(m, "quit job o/r#7")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "> quit job o/r#7") {
		t.Fatalf("typed request is not shown:\n%s", view)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(dashboard)
	if cmd == nil {
		t.Fatal("Enter did not send the request")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("sending the request reported %v", msg)
	}
	if got := <-sent; got != "quit job o/r#7" || len(m.prompt.input) != 0 {
		t.Fatalf("sent %q, input left %q", got, string(m.prompt.input))
	}
	updated, _ = m.Update(promptsMsg{{ID: 1, Request: "quit job o/r#7", Status: "done", Agent: "claude/opus", Response: "Stopping it.", Actions: []string{"stop o/r#7: done"}, Started: time.Now()}})
	m = updated.(dashboard)
	view := ansi.Strip(m.View())
	for _, want := range []string{"you: quit job o/r#7", "agent (claude/opus):", "Stopping it.", "-> stop o/r#7: done"} {
		if !strings.Contains(view, want) {
			t.Errorf("prompt area is missing %q:\n%s", want, view)
		}
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(dashboard)
	if m.prompt.open || strings.Contains(ansi.Strip(m.View()), "you: quit job") {
		t.Fatal("Esc did not close the prompt area")
	}
}

// An answer that arrives while the prompt area is closed is logged, so it is
// not missed.
func TestDashboardLogsAnswersWhileThePromptAreaIsClosed(t *testing.T) {
	m := newDashboard(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(dashboard)
	updated, _ = m.Update(promptsMsg{{ID: 3, Request: "status?", Status: "pending"}})
	m = updated.(dashboard)
	updated, _ = m.Update(promptsMsg{{ID: 3, Request: "status?", Status: "done", Response: "fine"}})
	m = updated.(dashboard)
	if len(m.logs) != 1 || !strings.Contains(m.logs[0], `prompt agent answered "status?" (done); press p to read it`) {
		t.Fatalf("logs = %q", m.logs)
	}
}

func TestHelpListsThePromptKey(t *testing.T) {
	if !strings.Contains(strings.Join(helpLines, "\n"), "p           open the prompt agent") {
		t.Errorf("help does not list the prompt key: %q", helpLines)
	}
}
