// Package webui is glorp's browser dashboard: the HTTP server that publishes a
// run's state and job actions, and the Vite frontend beside it that renders
// them. Both halves live here so the dashboard is one package rather than Go
// files in the root package serving assets built from a separate directory
// (issue #479).
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/lsegal/glorp/core"
)

// DefaultPort is the first localhost port the dashboard tries to bind.
const DefaultPort = 8765

// State is the JSON payload the dashboard's /api/state endpoint publishes. It
// is exported because `glorp ui` reads it back when probing localhost for
// running dashboards.
type State struct {
	Version  string        `json:"version"`
	Snapshot core.Snapshot `json:"snapshot"`
	Logs     []string      `json:"logs"`
}

// Server keeps the browser dashboard's state and serves its frontend.
type Server struct {
	mu       sync.RWMutex
	version  string
	snapshot core.Snapshot
	logs     []string
	assets   http.Handler
	action   func(context.Context, core.JobAction) error
	settings func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error)
}

// New builds a dashboard server that reports version.
func New(version string) (*Server, error) {
	return &Server{version: version, assets: newAssets()}, nil
}

// SetJobActionHandler wires the dashboard's retry and stop buttons to a
// function that performs them.
func (ui *Server) SetJobActionHandler(handler func(context.Context, core.JobAction) error) {
	ui.mu.Lock()
	ui.action = handler
	ui.mu.Unlock()
}

// SetSettingsHandler wires the modal dialog (issue #341) to a function that
// reads or applies live-editable runtime settings.
func (ui *Server) SetSettingsHandler(handler func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error)) {
	ui.mu.Lock()
	ui.settings = handler
	ui.mu.Unlock()
}

func (ui *Server) Snapshot(snapshot core.Snapshot) {
	ui.mu.Lock()
	ui.snapshot = snapshot
	ui.mu.Unlock()
}

func (ui *Server) Log(line string) {
	ui.mu.Lock()
	ui.logs = append(ui.logs, line)
	if len(ui.logs) > 200 {
		ui.logs = ui.logs[len(ui.logs)-200:]
	}
	ui.mu.Unlock()
}

func (ui *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/state" {
		ui.serveState(w, r)
		return
	}
	if r.URL.Path == "/api/jobs/action" {
		ui.serveJobAction(w, r)
		return
	}
	if r.URL.Path == "/api/settings" {
		ui.serveSettings(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	ui.assets.ServeHTTP(w, r)
}

func (ui *Server) serveJobAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var action core.JobAction
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&action); err != nil || action.Number <= 0 || action.Target == "" || (action.Action != "retry" && action.Action != "stop") {
		http.Error(w, "invalid job action", http.StatusBadRequest)
		return
	}
	ui.mu.RLock()
	handler := ui.action
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "job actions unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := handler(r.Context(), action); err != nil {
		if errors.Is(err, core.ErrNotReady) {
			http.Error(w, "job actions unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveSettings backs the settings modal (issue #341). GET reports the
// current live-editable settings; POST applies a partial update and reports
// the resulting settings.
func (ui *Server) serveSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var update core.SettingsUpdate
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&update); err != nil {
			http.Error(w, "invalid settings update", http.StatusBadRequest)
			return
		}
	}
	ui.mu.RLock()
	handler := ui.settings
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
		return
	}
	snapshot, err := handler(r.Context(), update)
	if err != nil {
		if errors.Is(err, core.ErrNotReady) {
			http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (ui *Server) serveState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.RLock()
	state := State{Version: ui.version, Snapshot: ui.snapshot, Logs: append([]string(nil), ui.logs...)}
	ui.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(state)
}

// Listen binds the first free localhost port at or above startPort and reports
// the listener along with the port it took.
func Listen(startPort int) (net.Listener, int, error) {
	if startPort < 1 || startPort > 65535 {
		return nil, 0, fmt.Errorf("web-ui-port must be between 1 and 65535")
	}
	for port := startPort; port <= 65535; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return listener, port, nil
		}
		if !strings.Contains(strings.ToLower(err.Error()), "address already in use") &&
			!strings.Contains(strings.ToLower(err.Error()), "only one usage") {
			return nil, 0, fmt.Errorf("listen for web UI on port %d: %w", port, err)
		}
	}
	return nil, 0, fmt.Errorf("no web UI port available at or above %d", startPort)
}

// FrontendDir is where this package's Vite project sits relative to the
// repository root, which is the working directory a development build runs
// from.
const FrontendDir = "webui"

// Supervisor starts and stops the Vite dev server. glorp tracks every
// subprocess it owns so none outlive the run (issue #260), so the caller
// supplies that tracking rather than this package spawning processes behind
// its back.
type Supervisor interface {
	Start(*exec.Cmd) error
	Run(*exec.Cmd) error
	Stop(*exec.Cmd) error
}
