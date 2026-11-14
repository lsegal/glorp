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
	"time"

	"github.com/lsegal/glorp/core"
)

// DefaultPort is the first localhost port the dashboard tries to bind.
const DefaultPort = 8765

// DefaultBind is the address the dashboard listens on unless told otherwise.
// Loopback keeps it private to this machine: it has no authentication, and it
// can stop jobs and change settings (issue #666).
const DefaultBind = "127.0.0.1"

// State is the JSON payload the dashboard's /api/state endpoint publishes. It
// is exported because `glorp ui` reads it back when probing localhost for
// running dashboards.
type State struct {
	Version  string        `json:"version"`
	Snapshot core.Snapshot `json:"snapshot"`
	Logs     []string      `json:"logs"`
	// Update is the newer glorp release the updater downloaded and the
	// dashboard offers to install (issue #783), or empty when glorp is
	// current.
	Update string `json:"update,omitempty"`
	// Prompts is the prompt area's conversation with the prompt agent, oldest
	// first (issue #801).
	Prompts []core.PromptEntry `json:"prompts,omitempty"`
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
	agents   func(context.Context) ([]core.AgentStatus, error)
	// unblock and issueUpdate back the MCP tools that run a watchdog round
	// and post an update comment (issue #806).
	unblock     func(key string) error
	issueUpdate func(context.Context, core.IssueUpdate) (core.IssueUpdateResult, error)
	// ask and createIssue back the MCP tools that run the prompt agent and
	// file an issue through gh-create (issue #807).
	ask         func(ctx context.Context, request, model string) (core.PromptEntry, error)
	createIssue func(context.Context, core.IssueCreate) (core.IssueCreateResult, error)
	refresh     func(context.Context) error
	restart     func(context.Context) ([]core.CheckpointResult, error)
	// restartProgress reports the shutdown checkpoint while a restart is
	// taking it (issue #749).
	restartProgress func() []core.CheckpointResult
	// update installs the downloaded glorp release and restarts onto it, and
	// updateVersion names that release (issue #783).
	update        func(context.Context) ([]core.CheckpointResult, error)
	updateVersion string
	// prompt starts answering a prompt area request, and prompts is the
	// conversation it reported last (issue #801).
	prompt  func(string) (core.PromptEntry, error)
	prompts []core.PromptEntry
	// agentStatuses is the most recent completed probe. Probing starts as soon
	// as the handler is wired, rather than making the first settings modal wait
	// for every installed CLI to answer (issue #595).
	agentStatuses    []core.AgentStatus
	agentsProbed     bool
	agentsRefreshing bool
	// agentsProbedAt is when agentStatuses was probed. A report older than
	// agentsMaxAge is still served at once, and refreshed in the background
	// for the next read (issue #803).
	agentsProbedAt time.Time
	agentsMaxAge   time.Duration
}

// AgentsRefreshingHeader marks an /api/agents response served from the cache
// while a fresh probe runs, so the settings modal knows to read it again and
// update the probed fields in place (issue #803).
const AgentsRefreshingHeader = "X-Glorp-Agents-Refreshing"

// agentStatusesMaxAge is how long an agent probe stays current. Probing runs
// every installed CLI and reads quotas, so it is not repeated on every read.
const agentStatusesMaxAge = 5 * time.Minute

// New builds a dashboard server that reports version.
func New(version string) (*Server, error) {
	return &Server{version: version, assets: newAssets(), agentsMaxAge: agentStatusesMaxAge}, nil
}

// SetJobActionHandler wires the dashboard's retry, stop, and pause buttons to a
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

// SetRefreshHandler wires the dashboard's refresh button (issue #646) to a
// function that forces an immediate repoll of GitHub instead of waiting for
// the run's normal poll interval.
func (ui *Server) SetRefreshHandler(handler func(context.Context) error) {
	ui.mu.Lock()
	ui.refresh = handler
	ui.mu.Unlock()
}

// SetRestartHandler wires the dashboard's restart button (issue #665) to a
// function that shuts the running instance down and starts it again with the
// same command line.
func (ui *Server) SetRestartHandler(handler func(context.Context) ([]core.CheckpointResult, error)) {
	ui.mu.Lock()
	ui.restart = handler
	ui.mu.Unlock()
}

// SetRestartProgressHandler wires GET /api/restart to a function that reports
// each run's shutdown checkpoint as it stands, so the dashboard can show which
// runs are done and which are still pending while a restart waits on them
// (issue #749).
func (ui *Server) SetRestartProgressHandler(handler func() []core.CheckpointResult) {
	ui.mu.Lock()
	ui.restartProgress = handler
	ui.mu.Unlock()
}

// SetUpdateHandler wires the dashboard's update button (issue #783) to a
// function that installs the downloaded glorp release and restarts onto it.
func (ui *Server) SetUpdateHandler(handler func(context.Context) ([]core.CheckpointResult, error)) {
	ui.mu.Lock()
	ui.update = handler
	ui.mu.Unlock()
}

// SetPromptHandler wires the prompt area (issue #801) to a function that
// starts answering a request and returns its pending entry without waiting
// for the answer.
func (ui *Server) SetPromptHandler(handler func(string) (core.PromptEntry, error)) {
	ui.mu.Lock()
	ui.prompt = handler
	ui.mu.Unlock()
}

// Prompts records the prompt area's conversation, which /api/state publishes.
func (ui *Server) Prompts(entries []core.PromptEntry) {
	ui.mu.Lock()
	ui.prompts = entries
	ui.mu.Unlock()
}

// SetUpdate records that glorp version is downloaded and ready to install, so
// the dashboard shows its update notice.
func (ui *Server) SetUpdate(version string) {
	ui.mu.Lock()
	ui.updateVersion = version
	ui.mu.Unlock()
}

// SetAgentsHandler wires the settings modal's agents tab (issue #572) to a
// function that probes every registered agent for its install, sign-in, and
// quota state, mirroring what `glorp agents` prints.
func (ui *Server) SetAgentsHandler(handler func(context.Context) ([]core.AgentStatus, error)) {
	ui.mu.Lock()
	ui.agents = handler
	ui.mu.Unlock()
	ui.refreshAgentStatuses()
}

// refreshAgentStatuses starts one background refresh when one is not already
// running. A completed result is retained so later settings-modal opens do
// not repeat the slow CLI probes.
func (ui *Server) refreshAgentStatuses() {
	ui.startAgentsRefresh(true)
}

// refreshStaleAgentStatuses starts a background refresh only when the last
// probe is missing or stale. It rechecks that under the lock that marks the
// refresh running, so a probe that finished after the caller looked does not
// start a redundant one (issue #813).
func (ui *Server) refreshStaleAgentStatuses() {
	ui.startAgentsRefresh(false)
}

func (ui *Server) startAgentsRefresh(force bool) {
	ui.mu.Lock()
	if ui.agents == nil || ui.agentsRefreshing || !force && !ui.agentsStaleLocked() {
		ui.mu.Unlock()
		return
	}
	handler := ui.agents
	ui.agentsRefreshing = true
	ui.mu.Unlock()
	go func() {
		statuses, err := handler(context.Background())
		ui.mu.Lock()
		defer ui.mu.Unlock()
		ui.agentsRefreshing = false
		if err == nil {
			ui.agentStatuses = statuses
			ui.agentsProbed = true
			ui.agentsProbedAt = time.Now()
		}
	}()
}

// agentsStaleLocked reports whether the agent statuses were never probed or are
// older than agentsMaxAge. The caller holds ui.mu.
func (ui *Server) agentsStaleLocked() bool {
	return !ui.agentsProbed || time.Since(ui.agentsProbedAt) > ui.agentsMaxAge
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
	if r.URL.Path == "/api/refresh" {
		ui.serveRefresh(w, r)
		return
	}
	if r.URL.Path == "/api/restart" {
		ui.serveRestart(w, r)
		return
	}
	if r.URL.Path == "/api/update" {
		ui.serveUpdate(w, r)
		return
	}
	if r.URL.Path == "/api/settings" {
		ui.serveSettings(w, r)
		return
	}
	if r.URL.Path == "/api/agents" {
		ui.serveAgents(w, r)
		return
	}
	if r.URL.Path == "/api/prompt" {
		ui.servePrompt(w, r)
		return
	}
	if r.URL.Path == MCPPath {
		ui.serveMCP(w, r)
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
	if err := decoder.Decode(&action); err != nil || action.Number <= 0 || action.Target == "" || (action.Action != "retry" && action.Action != "stop" && action.Action != "pause") {
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

// servePrompt backs the prompt area (issue #801). It only starts the agent
// call and answers 202 with the request's pending entry; the answer arrives
// later through /api/state, so no request ever waits on the agent or on the
// run loop.
func (ui *Server) servePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Request string `json:"request"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Request) == "" {
		http.Error(w, "invalid prompt request", http.StatusBadRequest)
		return
	}
	ui.mu.RLock()
	handler := ui.prompt
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "prompt unavailable", http.StatusServiceUnavailable)
		return
	}
	entry, err := handler(body.Request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(entry)
}

// serveRefresh backs the dashboard's refresh button (issue #646). It carries
// no request body: the button only ever asks for "poll now", never a
// specific target, so there is nothing to decode.
func (ui *Server) serveRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.RLock()
	handler := ui.refresh
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "refresh unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := handler(r.Context()); err != nil {
		if errors.Is(err, core.ErrNotReady) {
			http.Error(w, "refresh unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveRestart backs the dashboard's restart button (issue #665). Like
// refresh it carries no request body. The handler takes the shutdown
// checkpoint (issue #741) before answering, so the response reports each
// active run's outcome; the shutdown itself happens after this response is
// written, so the browser learns the outcome before the server goes away.
func (ui *Server) serveRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		ui.serveRestartProgress(w)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.RLock()
	handler := ui.restart
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "restart unavailable", http.StatusServiceUnavailable)
		return
	}
	checkpoints, err := handler(r.Context())
	if err != nil {
		if errors.Is(err, core.ErrNotReady) {
			http.Error(w, "restart unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeCheckpoints(w, checkpoints)
}

// serveUpdate backs the dashboard's update button (issue #783). It installs
// the downloaded release and starts the same restart the restart button does,
// answering with each run's shutdown checkpoint as POST /api/restart does;
// GET /api/restart reports its progress meanwhile.
func (ui *Server) serveUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.RLock()
	handler := ui.update
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "update unavailable", http.StatusServiceUnavailable)
		return
	}
	checkpoints, err := handler(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeCheckpoints(w, checkpoints)
}

// serveRestartProgress reports the shutdown checkpoint a restart is taking,
// with runs still going marked pending, or no runs before one starts.
func (ui *Server) serveRestartProgress(w http.ResponseWriter) {
	ui.mu.RLock()
	progress := ui.restartProgress
	ui.mu.RUnlock()
	var checkpoints []core.CheckpointResult
	if progress != nil {
		checkpoints = progress()
	}
	writeCheckpoints(w, checkpoints)
}

func writeCheckpoints(w http.ResponseWriter, checkpoints []core.CheckpointResult) {
	if checkpoints == nil {
		checkpoints = []core.CheckpointResult{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Checkpoints []core.CheckpointResult
	}{checkpoints})
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

// serveAgents backs the settings modal's agents tab (issue #572). It reports
// every registered agent's install, sign-in, and quota state, the same
// information `glorp agents` prints to the terminal. It never probes while
// answering: it serves the last background probe, starting a new one when
// that is missing or stale (issue #803).
func (ui *Server) serveAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.RLock()
	handler := ui.agents
	ui.mu.RUnlock()
	if handler == nil {
		http.Error(w, "agents unavailable", http.StatusServiceUnavailable)
		return
	}
	ui.mu.RLock()
	statuses := append([]core.AgentStatus(nil), ui.agentStatuses...)
	probed := ui.agentsProbed
	ui.mu.RUnlock()
	ui.refreshStaleAgentStatuses()
	if !probed {
		http.Error(w, "agents are still being probed", http.StatusServiceUnavailable)
		return
	}
	ui.mu.RLock()
	refreshing := ui.agentsRefreshing
	ui.mu.RUnlock()
	if refreshing {
		w.Header().Set(AgentsRefreshingHeader, "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(statuses)
}

func (ui *Server) serveState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	ui.mu.RLock()
	state := State{Version: ui.version, Snapshot: ui.snapshot, Logs: append([]string(nil), ui.logs...), Update: ui.updateVersion, Prompts: ui.prompts}
	ui.mu.RUnlock()
	state.Snapshot.Jobs = pageLogs(state.Snapshot.Jobs, state.Snapshot.VisibleJobs, page)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(state)
}

// pageLogs copies jobs keeping the Log only on the jobs of the zero-based page,
// perPage jobs at a time. Each Log is an agent's whole transcript, so only the
// page the dashboard is showing carries them (issues #714 and #739). A page
// outside the list, or no page at all, means the first page; a perPage of zero
// publishes every Log.
func pageLogs(jobs []core.JobSnapshot, perPage, page int) []core.JobSnapshot {
	if perPage <= 0 || len(jobs) == 0 {
		return jobs
	}
	if page < 0 || page*perPage >= len(jobs) {
		page = 0
	}
	start, end := page*perPage, min((page+1)*perPage, len(jobs))
	list := append([]core.JobSnapshot(nil), jobs...)
	for i := range list {
		if i < start || i >= end {
			list[i].Log = ""
		}
	}
	return list
}

// Listen binds the first free port on host at or above startPort and reports
// the listener along with the port it took. An empty host listens on every
// interface, as "0.0.0.0" does.
func Listen(host string, startPort int) (net.Listener, int, error) {
	if startPort < 1 || startPort > 65535 {
		return nil, 0, fmt.Errorf("web-ui-port must be between 1 and 65535")
	}
	if err := ValidateBind(host); err != nil {
		return nil, 0, err
	}
	host = strings.Trim(host, "[]")
	for port := startPort; port <= 65535; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
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

// ValidateBind rejects a bind address that carries a port, since the port
// comes from --web-ui-port. A bare IPv6 address such as "::" is accepted.
func ValidateBind(host string) error {
	if strings.Contains(host, ":") && net.ParseIP(strings.Trim(host, "[]")) == nil {
		return fmt.Errorf("bind must be a host address without a port, such as 0.0.0.0; got %q", host)
	}
	return nil
}

// IsLoopback reports whether a bind address keeps the dashboard private to
// this machine.
func IsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// URL is the address to open the dashboard at when it listens on host and
// port. Loopback and wildcard binds are both reachable as localhost, which is
// also where `glorp ui` looks; any other host is only reachable by name.
func URL(host string, port int) string {
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); host == "" || IsLoopback(host) || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
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
