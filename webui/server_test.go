package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
)

func TestListenUsesNextAvailablePort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	start := occupied.Addr().(*net.TCPAddr).Port

	listener, port, err := Listen(DefaultBind, start)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if port <= start {
		t.Fatalf("port = %d, want a port after occupied port %d", port, start)
	}
}

func TestListenRejectsInvalidPort(t *testing.T) {
	if _, _, err := Listen(DefaultBind, 0); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("error = %v, want port range error", err)
	}
}

func TestListenBindsRequestedHost(t *testing.T) {
	for bind, wantLoopback := range map[string]bool{"0.0.0.0": false, DefaultBind: true} {
		listener, _, err := Listen(bind, DefaultPort)
		if err != nil {
			t.Fatal(err)
		}
		ip := listener.Addr().(*net.TCPAddr).IP
		listener.Close()
		if ip.IsLoopback() != wantLoopback || (!wantLoopback && !ip.IsUnspecified()) {
			t.Fatalf("Listen(%q) bound %v", bind, ip)
		}
	}
}

func TestListenRejectsBindWithPort(t *testing.T) {
	if _, _, err := Listen("0.0.0.0:80", DefaultPort); err == nil || !strings.Contains(err.Error(), "without a port") {
		t.Fatalf("error = %v, want bind-with-port error", err)
	}
	for _, bind := range []string{"", "::", "[::1]", "localhost", "0.0.0.0"} {
		if err := ValidateBind(bind); err != nil {
			t.Fatalf("ValidateBind(%q) = %v, want accepted", bind, err)
		}
	}
}

func TestURLNamesReachableHost(t *testing.T) {
	for bind, want := range map[string]string{
		"127.0.0.1":   "http://localhost:8765",
		"localhost":   "http://localhost:8765",
		"0.0.0.0":     "http://localhost:8765",
		"::":          "http://localhost:8765",
		"":            "http://localhost:8765",
		"192.168.1.5": "http://192.168.1.5:8765",
		"fd00::1":     "http://[fd00::1]:8765",
	} {
		if got := URL(bind, 8765); got != want {
			t.Errorf("URL(%q) = %q, want %q", bind, got, want)
		}
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

func TestServerHandlesRefresh(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	ui.SetRefreshHandler(func(context.Context) error {
		called = true
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("POST refresh = %d, body = %q", response.Code, response.Body.String())
	}
	if !called {
		t.Fatal("refresh handler was not called")
	}
}

func TestServerRejectsUnavailableRefresh(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST refresh = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

// TestServerReportsNotReadyRefreshAsUnavailable mirrors
// TestServerReportsNotReadyJobActionsAsUnavailable for the refresh endpoint
// (issue #646).
func TestServerReportsNotReadyRefreshAsUnavailable(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetRefreshHandler(func(context.Context) error {
		return core.ErrNotReady
	})
	request := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST refresh = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestServerRejectsUnsupportedRefreshMethod(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/refresh", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET refresh = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestServerHandlesRestart(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	ui.SetRestartHandler(func(context.Context) error {
		called = true
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("POST restart = %d, body = %q", response.Code, response.Body.String())
	}
	if !called {
		t.Fatal("restart handler was not called")
	}
}

func TestServerRejectsUnavailableRestart(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST restart = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestServerReportsRestartErrors(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetRestartHandler(func(context.Context) error {
		return core.ErrNotReady
	})
	request := httptest.NewRequest(http.MethodPost, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST restart (not ready) = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	ui.SetRestartHandler(func(context.Context) error {
		return errors.New("already restarting")
	})
	response = httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/restart", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "already restarting") {
		t.Fatalf("POST restart (error) = %d %q, want %d", response.Code, response.Body.String(), http.StatusConflict)
	}
}

func TestServerRejectsUnsupportedRestartMethod(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET restart = %d, want %d", response.Code, http.StatusMethodNotAllowed)
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

// TestServerHandlesAgents checks the settings modal's agents tab (issue
// #572) reads back exactly what the run's agent probe reports.
func TestServerHandlesAgents(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	statuses := []core.AgentStatus{
		{Name: "codex", Installed: true, Auth: "signed in", Quota: "80% left", Status: "ok"},
		{Name: "claude", Installed: false, Auth: "unknown", Status: "missing"},
	}
	ui.SetAgentsHandler(func(context.Context) ([]core.AgentStatus, error) { return statuses, nil })
	request := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	deadline := time.Now().Add(time.Second)
	var response *httptest.ResponseRecorder
	for {
		response = httptest.NewRecorder()
		ui.ServeHTTP(response, request)
		if response.Code == http.StatusOK || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("GET agents = %d, body = %q", response.Code, response.Body.String())
	}
	var got []core.AgentStatus
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "codex" || got[1].Status != "missing" {
		t.Fatalf("agents = %#v", got)
	}
}

func TestServerWarmsAndCachesAgents(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	ui.SetAgentsHandler(func(context.Context) ([]core.AgentStatus, error) {
		close(started)
		<-release
		return []core.AgentStatus{{Name: "codex"}}, nil
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background agent probe did not start")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("warming GET agents = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for response.Code != http.StatusOK && time.Now().Before(deadline) {
		response = httptest.NewRecorder()
		ui.ServeHTTP(response, request)
		time.Sleep(time.Millisecond)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("cached GET agents = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRejectsUnavailableAgents(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET agents = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestServerRejectsAgentsPost(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetAgentsHandler(func(context.Context) ([]core.AgentStatus, error) { return nil, nil })
	request := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST agents = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}
