package main

import (
	"github.com/lsegal/glorp/webui"

	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// dashboardServer starts a stub that answers /api/state the way the real web
// UI does, so probing sees a genuine glorp dashboard.
func dashboardServer(t *testing.T, snapshot GlorpSnapshot) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/state" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(webui.State{Snapshot: snapshot, Logs: []string{}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProbeDashboardReadsSnapshot(t *testing.T) {
	server := dashboardServer(t, GlorpSnapshot{Targets: []string{"owner/repo"}, Running: 2, Queued: 1, Completed: 4, Failed: 1})
	found, ok := probeDashboard(context.Background(), server.Client(), server.URL, 8765)
	if !ok {
		t.Fatal("probe failed on a real dashboard")
	}
	if found.Port != 8765 || found.Running != 2 || found.Queued != 1 || found.Complete != 4 || found.Failed != 1 {
		t.Fatalf("dashboard = %+v, want the served snapshot", found)
	}
	if len(found.Targets) != 1 || found.Targets[0] != "owner/repo" {
		t.Fatalf("targets = %v, want [owner/repo]", found.Targets)
	}
	if found.URL() != "http://localhost:8765" {
		t.Fatalf("URL = %q", found.URL())
	}
}

func TestProbeDashboardIgnoresOtherServices(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"not found": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"html": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "<html><body>some other local app</body></html>")
		},
		"unrelated json": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"status":"ok"}`)
		},
		"json without logs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"snapshot":{}}`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			if _, ok := probeDashboard(context.Background(), server.Client(), server.URL, 8765); ok {
				t.Fatal("probe accepted a server that is not a glorp dashboard")
			}
		})
	}
}

func TestProbeDashboardIgnoresClosedPort(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	if _, ok := probeDashboard(context.Background(), http.DefaultClient, url, 8765); ok {
		t.Fatal("probe accepted a closed port")
	}
}

func TestDiscoverDashboardsScansPortRangeInOrder(t *testing.T) {
	first := dashboardServer(t, GlorpSnapshot{Targets: []string{"owner/first"}})
	second := dashboardServer(t, GlorpSnapshot{Targets: []string{"owner/second"}})
	urls := map[int]string{8766: second.URL, 8765: first.URL}
	baseURL := func(port int) string {
		if url, ok := urls[port]; ok {
			return url
		}
		// An address nothing is listening on stands in for an unused port.
		return "http://127.0.0.1:1"
	}
	found := discoverDashboards(context.Background(), http.DefaultClient, baseURL, []int{8765, 8766, 8767, 8768})
	if len(found) != 2 {
		t.Fatalf("found %d dashboards, want 2", len(found))
	}
	if found[0].Port != 8765 || found[1].Port != 8766 {
		t.Fatalf("ports = %d,%d, want them sorted ascending", found[0].Port, found[1].Port)
	}
	if found[0].Targets[0] != "owner/first" || found[1].Targets[0] != "owner/second" {
		t.Fatalf("dashboards = %+v, want each port matched to its own instance", found)
	}
}

func TestSelectDashboardWithoutInstancesReportsError(t *testing.T) {
	var out bytes.Buffer
	selected, err := selectDashboard(nil, 8765, true, &out)
	if selected != nil || err == nil {
		t.Fatalf("selected = %v, err = %v, want an error", selected, err)
	}
	if !strings.Contains(err.Error(), "glorp watch") {
		t.Fatalf("err = %v, want it to suggest glorp watch", err)
	}
}

func TestSelectDashboardOpensTheOnlyInstance(t *testing.T) {
	var out bytes.Buffer
	found := []dashboardInstance{{Port: 9001}}
	selected, err := selectDashboard(found, 8765, false, &out)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if selected == nil || selected.Port != 9001 {
		t.Fatalf("selected = %+v, want port 9001", selected)
	}
}

func TestSelectDashboardWithoutTerminalTakesFirstInstance(t *testing.T) {
	var out bytes.Buffer
	found := []dashboardInstance{{Port: 8765}, {Port: 8766}}
	selected, err := selectDashboard(found, 8765, false, &out)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if selected == nil || selected.Port != 8765 {
		t.Fatalf("selected = %+v, want the lowest port", selected)
	}
	if !strings.Contains(out.String(), "port 8765") {
		t.Fatalf("output = %q, want it to say which dashboard opened", out.String())
	}
}

func TestDashboardLabelDescribesInstance(t *testing.T) {
	labeled := dashboardInstance{Port: 8765, Targets: []string{"owner/repo", "owner/other"}, Running: 2, Queued: 3}
	label := labeled.Label()
	for _, want := range []string{"8765", "owner/repo", "owner/other", "2 running", "3 queued"} {
		if !strings.Contains(label, want) {
			t.Fatalf("label = %q, want it to contain %q", label, want)
		}
	}
	if empty := (dashboardInstance{Port: 8765}).Label(); !strings.Contains(empty, "no targets") {
		t.Fatalf("label = %q, want a placeholder for an instance with no targets", empty)
	}
}

func TestDashboardPickerMovesAndSelects(t *testing.T) {
	picker := newDashboardPicker([]dashboardInstance{{Port: 8765}, {Port: 8766}, {Port: 8767}})
	press := func(model dashboardPicker, key string) dashboardPicker {
		t.Helper()
		updated, _ := model.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune(key)}))
		return updated.(dashboardPicker)
	}
	pressed := press(picker, "j")
	if pressed.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after moving down", pressed.cursor)
	}
	pressed = press(pressed, "k")
	if pressed.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after moving up", pressed.cursor)
	}
	// The cursor must not run off either end of the list.
	pressed = press(press(pressed, "k"), "k")
	if pressed.cursor != 0 {
		t.Fatalf("cursor = %d, want it clamped at the top", pressed.cursor)
	}
	pressed = press(press(press(press(pressed, "j"), "j"), "j"), "j")
	if pressed.cursor != 2 {
		t.Fatalf("cursor = %d, want it clamped at the bottom", pressed.cursor)
	}
	chosen, _ := pressed.Update(tea.KeyMsg(tea.Key{Type: tea.KeyEnter}))
	selected := chosen.(dashboardPicker)
	if selected.chosen != 2 || selected.quit {
		t.Fatalf("picker = %+v, want the third instance chosen", selected)
	}
	if !strings.Contains(selected.View(), "8767") {
		t.Fatalf("view = %q, want it to list every instance", selected.View())
	}
}

func TestDashboardPickerCancels(t *testing.T) {
	picker := newDashboardPicker([]dashboardInstance{{Port: 8765}, {Port: 8766}})
	updated, _ := picker.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("q")}))
	quit := updated.(dashboardPicker)
	if !quit.quit || quit.chosen != -1 {
		t.Fatalf("picker = %+v, want a cancelled selection", quit)
	}
}

func TestBrowserCommandPerPlatform(t *testing.T) {
	cases := map[string][]string{
		"darwin":  {"open", "http://localhost:8765"},
		"windows": {"cmd", "/c", "start", "", "http://localhost:8765"},
		"linux":   {"xdg-open", "http://localhost:8765"},
	}
	for goos, want := range cases {
		got := browserCommand(goos, "http://localhost:8765").Args
		if len(got) != len(want) {
			t.Fatalf("%s args = %v, want %v", goos, got, want)
		}
		for i := range want[1:] {
			if got[i+1] != want[i+1] {
				t.Fatalf("%s args = %v, want %v", goos, got, want)
			}
		}
		if !strings.HasSuffix(got[0], want[0]) && got[0] != want[0] {
			t.Fatalf("%s binary = %q, want %q", goos, got[0], want[0])
		}
	}
}
