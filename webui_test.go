package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestListenForWebUIUsesNextAvailablePort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	start := occupied.Addr().(*net.TCPAddr).Port

	listener, port, err := listenForWebUI(start)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if port <= start {
		t.Fatalf("port = %d, want a port after occupied port %d", port, start)
	}
}

func TestListenForWebUIRejectsInvalidPort(t *testing.T) {
	if _, _, err := listenForWebUI(0); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("error = %v, want port range error", err)
	}
}

func TestWebUIStateIncludesSnapshotsAndBoundedLogs(t *testing.T) {
	ui, err := NewWebUI()
	if err != nil {
		t.Fatal(err)
	}
	ui.Snapshot(GlorpSnapshot{Running: 2, Targets: []string{"owner/repo"}})
	for i := 0; i < 205; i++ {
		ui.Log("line " + strconv.Itoa(i))
	}

	request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	var state webUIState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || state.Snapshot.Running != 2 || len(state.Logs) != 200 {
		t.Fatalf("response = %d, state = %#v", response.Code, state)
	}
	if state.Logs[0] != "line 5" || state.Logs[199] != "line 204" {
		t.Fatalf("bounded logs = %q ... %q", state.Logs[0], state.Logs[199])
	}
}

func TestWebUIServesReactAppAndSPAFallback(t *testing.T) {
	ui, err := NewWebUI()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/dashboard"} {
		response := httptest.NewRecorder()
		ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		body, _ := io.ReadAll(response.Result().Body)
		if response.Code != http.StatusOK || !strings.Contains(string(body), "<div id=\"root\"></div>") {
			t.Fatalf("GET %s = %d, %q", path, response.Code, body)
		}
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
