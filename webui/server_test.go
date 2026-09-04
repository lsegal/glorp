package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lsegal/glorp/core"
)

func TestListenUsesNextAvailablePort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	start := occupied.Addr().(*net.TCPAddr).Port

	listener, port, err := Listen(start)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if port <= start {
		t.Fatalf("port = %d, want a port after occupied port %d", port, start)
	}
}

func TestListenRejectsInvalidPort(t *testing.T) {
	if _, _, err := Listen(0); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("error = %v, want port range error", err)
	}
}

func TestStateIncludesSnapshotsAndBoundedLogs(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.Snapshot(core.Snapshot{Running: 2, Targets: []string{"owner/repo"}})
	for i := 0; i < 205; i++ {
		ui.Log("line " + strconv.Itoa(i))
	}

	request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	var state State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || state.Snapshot.Running != 2 || len(state.Logs) != 200 {
		t.Fatalf("response = %d, state = %#v", response.Code, state)
	}
	if state.Logs[0] != "line 5" || state.Logs[199] != "line 204" {
		t.Fatalf("bounded logs = %q ... %q", state.Logs[0], state.Logs[199])
	}
	if state.Version != "v1.2.3" {
		t.Fatalf("state.Version = %q, want %q", state.Version, "v1.2.3")
	}
}

func TestServerRejectsUnsupportedMethods(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST / = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestServerHandlesJobActions(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var got core.JobAction
	ui.SetJobActionHandler(func(_ context.Context, action core.JobAction) error {
		got = action
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/action", bytes.NewBufferString(`{"action":"retry","target":"o/r","number":7}`))
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("POST action = %d, body = %q", response.Code, response.Body.String())
	}
	if got != (core.JobAction{Action: "retry", Target: "o/r", Number: 7}) {
		t.Fatalf("action = %#v", got)
	}
}

func TestServerRejectsUnavailableJobActions(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/action", bytes.NewBufferString(`{"action":"stop","target":"o/r","number":7}`))
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST action = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

// TestServerReportsNotReadyJobActionsAsUnavailable checks that a handler
// reporting core.ErrNotReady -- the run loop not having reached its
// dispatch select statement yet -- gets the same fast 503 a nil handler
// gets, rather than the generic 409 other handler errors get (issue #579).
func TestServerReportsNotReadyJobActionsAsUnavailable(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetJobActionHandler(func(_ context.Context, _ core.JobAction) error {
		return core.ErrNotReady
	})
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/action", bytes.NewBufferString(`{"action":"retry","target":"o/r","number":7}`))
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST action = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

// TestServerReportsNotReadySettingsAsUnavailable mirrors
// TestServerReportsNotReadyJobActionsAsUnavailable for the settings
// endpoint (issue #579).
func TestServerReportsNotReadySettingsAsUnavailable(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetSettingsHandler(func(_ context.Context, _ core.SettingsUpdate) (core.SettingsSnapshot, error) {
		return core.SettingsSnapshot{}, core.ErrNotReady
	})
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET settings = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
