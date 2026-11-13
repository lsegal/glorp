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

func TestStatePublishesLogsOnlyForTheRequestedPage(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]core.JobSnapshot, 14)
	for i := range jobs {
		jobs[i] = core.JobSnapshot{Number: i + 1, Target: "o/r", Log: "output " + strconv.Itoa(i+1)}
	}
	ui.Snapshot(core.Snapshot{Jobs: jobs, VisibleJobs: 6})

	cases := []struct {
		query      string
		start, end int
	}{
		{"", 0, 6},
		{"?page=0", 0, 6},
		{"?page=1", 6, 12},
		{"?page=2", 12, 14},
		{"?page=3", 0, 6},
		{"?page=-1", 0, 6},
		{"?page=x", 0, 6},
	}
	for _, tc := range cases {
		response := httptest.NewRecorder()
		ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/state"+tc.query, nil))
		var state State
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Snapshot.Jobs) != len(jobs) {
			t.Fatalf("%q published %d jobs, want all %d", tc.query, len(state.Snapshot.Jobs), len(jobs))
		}
		for i, job := range state.Snapshot.Jobs {
			if onPage := i >= tc.start && i < tc.end; onPage != (job.Log != "") {
				t.Fatalf("%q: job #%d published Log %q; want Logs only for jobs %d-%d", tc.query, job.Number, job.Log, tc.start+1, tc.end)
			}
		}
	}
	if jobs[13].Log == "" {
		t.Fatal("serving a page cleared the Log on the stored snapshot")
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

// TestServerAcceptsPauseJobAction checks that the dashboard's Pause button
// reaches the run's job-action handler like Retry and Stop (issue #771), and
// that an action the run does not know is still rejected.
func TestServerAcceptsPauseJobAction(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var got core.JobAction
	ui.SetJobActionHandler(func(_ context.Context, action core.JobAction) error {
		got = action
		return nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/jobs/action", bytes.NewBufferString(`{"action":"pause","target":"o/r","number":7}`))
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("POST pause = %d, body = %q", response.Code, response.Body.String())
	}
	if got != (core.JobAction{Action: "pause", Target: "o/r", Number: 7}) {
		t.Fatalf("action = %#v", got)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/jobs/action", bytes.NewBufferString(`{"action":"resume","target":"o/r","number":7}`))
	response = httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST resume = %d, want %d", response.Code, http.StatusBadRequest)
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
	ui.SetRestartHandler(func(context.Context) ([]core.CheckpointResult, error) {
		called = true
		return []core.CheckpointResult{
			{Target: "o/r", Number: 7, Branch: "fix/issue-7-x", Outcome: "pushed"},
			{Target: "o/r", Number: 8, Outcome: "failed", Detail: "remote origin is unreachable"},
		}, nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST restart = %d, body = %q", response.Code, response.Body.String())
	}
	if !called {
		t.Fatal("restart handler was not called")
	}
	// The restart reports each run's shutdown checkpoint (issue #741).
	var body struct{ Checkpoints []core.CheckpointResult }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode restart response %q: %v", response.Body.String(), err)
	}
	if len(body.Checkpoints) != 2 || body.Checkpoints[0].Outcome != "pushed" || body.Checkpoints[1].Detail != "remote origin is unreachable" {
		t.Fatalf("restart checkpoints = %+v", body.Checkpoints)
	}
}

func TestServerReportsEmptyRestartCheckpoints(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetRestartHandler(func(context.Context) ([]core.CheckpointResult, error) { return nil, nil })
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/restart", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"Checkpoints":[]}` {
		t.Fatalf("POST restart = %d %q", response.Code, response.Body.String())
	}
}

// While a restart waits on the shutdown checkpoint, GET reports each run as
// it stands, so a run still going shows as pending (issue #749).
func TestServerReportsRestartProgress(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/restart", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"Checkpoints":[]}` {
		t.Fatalf("GET restart before a restart = %d %q", response.Code, response.Body.String())
	}
	ui.SetRestartProgressHandler(func() []core.CheckpointResult {
		return []core.CheckpointResult{
			{Target: "o/r", Number: 7, Outcome: "pushed"},
			{Target: "o/r", Number: 8, Outcome: "pending"},
		}
	})
	response = httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/restart", nil))
	var body struct{ Checkpoints []core.CheckpointResult }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode restart progress %q: %v", response.Body.String(), err)
	}
	if response.Code != http.StatusOK || len(body.Checkpoints) != 2 || body.Checkpoints[1].Outcome != "pending" {
		t.Fatalf("GET restart = %d %+v", response.Code, body.Checkpoints)
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
	ui.SetRestartHandler(func(context.Context) ([]core.CheckpointResult, error) {
		return nil, core.ErrNotReady
	})
	request := httptest.NewRequest(http.MethodPost, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST restart (not ready) = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	ui.SetRestartHandler(func(context.Context) ([]core.CheckpointResult, error) {
		return nil, errors.New("already restarting")
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
	request := httptest.NewRequest(http.MethodDelete, "/api/restart", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE restart = %d, want %d", response.Code, http.StatusMethodNotAllowed)
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

// The dashboard publishes a downloaded update and its button installs it
// (issue #783).
func TestServerUpdate(t *testing.T) {
	ui, err := New("v1.5.22")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/update", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST update without a handler = %d", response.Code)
	}
	state := func() State {
		t.Helper()
		response := httptest.NewRecorder()
		ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/state", nil))
		var state State
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if got := state().Update; got != "" {
		t.Fatalf("update before one is found = %q", got)
	}
	ui.SetUpdate("v1.5.23")
	if got := state().Update; got != "v1.5.23" {
		t.Fatalf("update = %q, want v1.5.23", got)
	}
	calls := 0
	ui.SetUpdateHandler(func(context.Context) error {
		calls++
		if calls > 1 {
			return errors.New("glorp is already updating")
		}
		return nil
	})
	response = httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/update", nil))
	if response.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("POST update = %d, calls = %d", response.Code, calls)
	}
	response = httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/update", nil))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "already updating") {
		t.Fatalf("second POST update = %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/update", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET update = %d", response.Code)
	}
}
