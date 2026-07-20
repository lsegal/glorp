package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const defaultWebUIPort = 8765

//go:embed web/dist/*
var webUIAssets embed.FS

type webUIState struct {
	Snapshot GlorpSnapshot `json:"snapshot"`
	Logs     []string      `json:"logs"`
}

// WebUI keeps the browser dashboard's state and serves the embedded React SPA.
type WebUI struct {
	mu       sync.RWMutex
	snapshot GlorpSnapshot
	logs     []string
	assets   http.Handler
}

func NewWebUI() (*WebUI, error) {
	dist, err := fs.Sub(webUIAssets, "web/dist")
	if err != nil {
		return nil, fmt.Errorf("load web UI assets: %w", err)
	}
	return &WebUI{assets: http.FileServer(http.FS(dist))}, nil
}

func (ui *WebUI) Snapshot(snapshot GlorpSnapshot) {
	ui.mu.Lock()
	ui.snapshot = snapshot
	ui.mu.Unlock()
}

func (ui *WebUI) Log(line string) {
	ui.mu.Lock()
	ui.logs = append(ui.logs, line)
	if len(ui.logs) > 200 {
		ui.logs = ui.logs[len(ui.logs)-200:]
	}
	ui.mu.Unlock()
}

func (ui *WebUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/state" {
		ui.serveState(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" {
		if _, err := fs.Stat(webUIAssets, "web/dist"+r.URL.Path); err != nil {
			r.URL.Path = "/"
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	ui.assets.ServeHTTP(w, r)
}

func (ui *WebUI) serveState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.RLock()
	state := webUIState{Snapshot: ui.snapshot, Logs: append([]string(nil), ui.logs...)}
	ui.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(state)
}

func listenForWebUI(startPort int) (net.Listener, int, error) {
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

type multiUIReporter []UIReporter

func (reporters multiUIReporter) Snapshot(snapshot GlorpSnapshot) {
	for _, reporter := range reporters {
		if reporter != nil {
			reporter.Snapshot(snapshot)
		}
	}
}

func (reporters multiUIReporter) Log(line string) {
	for _, reporter := range reporters {
		if reporter != nil {
			reporter.Log(line)
		}
	}
}

func combineUIReporters(reporters ...UIReporter) UIReporter {
	combined := make(multiUIReporter, 0, len(reporters))
	for _, reporter := range reporters {
		if reporter != nil {
			combined = append(combined, reporter)
		}
	}
	if len(combined) == 0 {
		return nil
	}
	return combined
}
