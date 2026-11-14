package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/lsegal/glorp/core"
)

// MCPPath is where the dashboard's listener serves MCP (issue #806), so an MCP
// client connects to the same origin as the dashboard, such as
// http://127.0.0.1:8765/mcp.
const MCPPath = "/mcp"

// mcpProtocolVersions are the MCP revisions the server speaks, newest first.
// It answers each request with a single JSON response and never opens an
// event stream, which every one of them allows.
var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// mcpLogLines is how much of a job's scrollback get_job returns by default,
// and mcpExplainLines how much explain_job shows.
const (
	mcpLogLines     = 200
	mcpExplainLines = 20
	mcpStatusLogs   = 50
)

// SetUnblockHandler wires the MCP trigger_unblock tool to the stuck-job
// watchdog's on-demand round (issue #806).
func (ui *Server) SetUnblockHandler(handler func(context.Context, core.UnblockRequest) (core.UnblockResult, error)) {
	ui.mu.Lock()
	ui.unblock = handler
	ui.mu.Unlock()
}

// SetIssueUpdateHandler wires the MCP update_issue tool to a function that
// posts an update comment mentioning the issue's author (issue #806).
func (ui *Server) SetIssueUpdateHandler(handler func(context.Context, core.IssueUpdate) (core.IssueUpdateResult, error)) {
	ui.mu.Lock()
	ui.issueUpdate = handler
	ui.mu.Unlock()
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

// serveMCP is MCP's Streamable HTTP transport: each POST carries one JSON-RPC
// message, a request is answered with one JSON response, and a notification
// or response is accepted with no body. The server offers no stream of its
// own, so GET is refused as the transport allows.
func (ui *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if !mcpOriginAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: "request body could not be read"}})
		return
	}
	var request mcpRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{Code: -32700, Message: "parse error: " + err.Error()}})
		return
	}
	if request.Method == "" {
		// A response to a request the server never sends, or a malformed
		// message; either way there is nothing to answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if request.JSONRPC != "2.0" {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Error: &mcpError{Code: -32600, Message: `jsonrpc must be "2.0"`}})
		return
	}
	result, rpcErr := ui.handleMCP(r.Context(), request)
	writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: result, Error: rpcErr})
}

func writeMCP(w http.ResponseWriter, response mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(response)
}

// mcpOriginAllowed refuses browser requests from another site, which the
// transport requires so a web page cannot drive the instance through a
// visitor's browser. Clients that send no Origin, as MCP clients outside a
// browser do, and the dashboard's own origin are allowed.
func mcpOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (ui *Server) handleMCP(ctx context.Context, request mcpRequest) (any, *mcpError) {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := mcpProtocolVersions[0]
		if slices.Contains(mcpProtocolVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		ui.mu.RLock()
		glorpVersion := ui.version
		ui.mu.RUnlock()
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "glorp", "version": glorpVersion},
			"instructions":    "Tools for the running glorp instance whose dashboard serves this endpoint: read its status and jobs, retry, pause, resume, or stop a job as the dashboard buttons do, run a stuck-job watchdog round now, and post an update comment on an issue or pull request.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			return nil, &mcpError{Code: -32602, Message: "tools/call needs a tool name"}
		}
		tool, ok := mcpToolHandlers[params.Name]
		if !ok {
			return nil, &mcpError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", params.Name)}
		}
		var args mcpArguments
		if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
			decoder := json.NewDecoder(strings.NewReader(string(params.Arguments)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&args); err != nil {
				return mcpToolError(fmt.Errorf("invalid arguments: %v", err)), nil
			}
		}
		value, err := tool(ui, ctx, args)
		if err != nil {
			return mcpToolError(err), nil
		}
		return mcpToolResult(value), nil
	}
	return nil, &mcpError{Code: -32601, Message: fmt.Sprintf("method %q not found", request.Method)}
}

// mcpArguments is the union of every tool's arguments. Each tool reads the
// ones it declares and ignores the rest.
type mcpArguments struct {
	Target     string `json:"target,omitempty"`
	Number     int    `json:"number,omitempty"`
	Status     string `json:"status,omitempty"`
	Lines      *int   `json:"lines,omitempty"`
	Repository string `json:"repository,omitempty"`
	Text       string `json:"text,omitempty"`
}

func mcpToolResult(value any) map[string]any {
	text, _ := json.MarshalIndent(value, "", "  ")
	result := map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}
	if _, object := value.(map[string]any); object {
		result["structuredContent"] = value
	}
	return result
}

func mcpToolError(err error) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}
}

// mcpTool is one entry of tools/list.
type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func mcpSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

var (
	mcpNumberProperty = map[string]any{"type": "integer", "minimum": 1, "description": "The job's issue number."}
	mcpTargetProperty = map[string]any{"type": "string", "description": "The job's target, such as OWNER/REPO, needed only when more than one target has a job with this number."}
	mcpJobSchema      = mcpSchema([]string{"number"}, map[string]any{"number": mcpNumberProperty, "target": mcpTargetProperty})
	mcpReadOnly       = map[string]any{"readOnlyHint": true}
)

var mcpTools = []mcpTool{
	{Name: "get_status", Title: "Instance status", Description: "The glorp instance's version, identity, targets, job counts, polling, quotas, live settings, registered agents, and recent log lines.", InputSchema: mcpSchema(nil, map[string]any{}), Annotations: mcpReadOnly},
	{Name: "list_jobs", Title: "List jobs", Description: "Every job the instance has in flight, as the dashboard lists them, with the actions each one accepts. Scrollback is left out; use get_job for it.", InputSchema: mcpSchema(nil, map[string]any{"status": map[string]any{"type": "string", "description": "Only list jobs with this status, such as active, waiting, queued, paused, or stopping."}}), Annotations: mcpReadOnly},
	{Name: "get_job", Title: "Get a job", Description: "One job's full state, its agent session, and its scrollback.", InputSchema: mcpSchema([]string{"number"}, map[string]any{"number": mcpNumberProperty, "target": mcpTargetProperty, "lines": map[string]any{"type": "integer", "minimum": 0, "description": fmt.Sprintf("How many of the scrollback's last lines to return; 0 returns all of it. Defaults to %d.", mcpLogLines)}}), Annotations: mcpReadOnly},
	{Name: "explain_job", Title: "Explain a job", Description: "Why a job is paused, blocked, waiting, queued, or looks stuck, what will wake it, and its last output.", InputSchema: mcpJobSchema, Annotations: mcpReadOnly},
	{Name: "retry_job", Title: "Retry a job", Description: "The dashboard's Retry button: requeues the job so gh-fix reruns it with its saved session. On a paused job it clears the pause first, whatever it waited on.", InputSchema: mcpJobSchema},
	{Name: "pause_job", Title: "Pause a job", Description: "The dashboard's Pause button: holds an active, waiting, or queued job back as paused by owner. A running agent is stopped and its work pushed; nothing but a retry or resume picks it back up.", InputSchema: mcpJobSchema},
	{Name: "resume_job", Title: "Resume a paused job", Description: "Resumes a paused job, which is the dashboard's Retry button on a paused job: the pause is cleared and the job is requeued.", InputSchema: mcpJobSchema},
	{Name: "stop_job", Title: "Stop a job", Description: "The dashboard's Stop button: stops an active or waiting job's agent, or clears a paused job and releases its claim on the issue.", InputSchema: mcpJobSchema, Annotations: map[string]any{"destructiveHint": true}},
	{Name: "trigger_unblock", Title: "Run an unblock round", Description: "Runs a stuck-job watchdog round now, over every stuck job or the one job named, with the same agent call, history, and logging as a scheduled round. It reports the jobs the round covers once the round starts.", InputSchema: mcpSchema(nil, map[string]any{"number": map[string]any{"type": "integer", "minimum": 1, "description": "One job's issue number to run the round for; leave it out to cover every stuck job."}, "target": mcpTargetProperty})},
	{Name: "update_issue", Title: "Post an update on an issue", Description: "Posts a comment on an issue or pull request that mentions its author and this instance and carries the update text, so the owning job hears it through glorp's mention path. It returns the comment's URL and never edits the issue or pull request body.", InputSchema: mcpSchema([]string{"number", "text"}, map[string]any{"number": map[string]any{"type": "integer", "minimum": 1, "description": "The issue or pull request number."}, "repository": map[string]any{"type": "string", "description": "OWNER/REPO hosting it. Defaults to the repository of the instance's job with this number, or the instance's only watched repository."}, "text": map[string]any{"type": "string", "description": "The update to post."}})},
}

var mcpToolHandlers = map[string]func(*Server, context.Context, mcpArguments) (any, error){
	"get_status":  (*Server).mcpStatus,
	"list_jobs":   (*Server).mcpListJobs,
	"get_job":     (*Server).mcpGetJob,
	"explain_job": (*Server).mcpExplainJob,
	"retry_job": func(ui *Server, ctx context.Context, args mcpArguments) (any, error) {
		return ui.mcpJobAction(ctx, args, "retry")
	},
	"pause_job": func(ui *Server, ctx context.Context, args mcpArguments) (any, error) {
		return ui.mcpJobAction(ctx, args, "pause")
	},
	"resume_job": func(ui *Server, ctx context.Context, args mcpArguments) (any, error) {
		return ui.mcpJobAction(ctx, args, "resume")
	},
	"stop_job": func(ui *Server, ctx context.Context, args mcpArguments) (any, error) {
		return ui.mcpJobAction(ctx, args, "stop")
	},
	"trigger_unblock": (*Server).mcpTriggerUnblock,
	"update_issue":    (*Server).mcpUpdateIssue,
}

// mcpState is the dashboard state at the moment of a call, the same state
// /api/state publishes, but with every job's whole scrollback.
func (ui *Server) mcpState() State {
	ui.mu.RLock()
	defer ui.mu.RUnlock()
	return State{Version: ui.version, Snapshot: ui.snapshot, Logs: append([]string(nil), ui.logs...), Update: ui.updateVersion}
}

func (ui *Server) mcpStatus(ctx context.Context, _ mcpArguments) (any, error) {
	state := ui.mcpState()
	snapshot := state.Snapshot
	status := map[string]any{
		"version":     state.Version,
		"identity":    snapshot.Identity,
		"targets":     snapshot.Targets,
		"issueCounts": snapshot.IssueCounts,
		"jobs": map[string]int{
			"running":   snapshot.Running,
			"queued":    snapshot.Queued,
			"paused":    snapshot.Paused,
			"completed": snapshot.Completed,
			"failed":    snapshot.Failed,
		},
		"concurrency":  snapshot.Concurrency,
		"pollInterval": core.FormatInterval(snapshot.Interval),
		"useWebhooks":  snapshot.UseWebhooks,
		"webUIURL":     snapshot.WebUIURL,
		"quota":        snapshot.Quota,
		"quotas":       snapshot.Quotas,
		"tokensUsed":   snapshot.TokensUsed,
		"tokenLimit":   snapshot.TokenLimit,
		"recentLogs":   lastItems(state.Logs, mcpStatusLogs),
	}
	if state.Update != "" {
		status["updateAvailable"] = state.Update
	}
	if snapshot.UseWebhooks {
		status["webhookURL"] = snapshot.WebhookURL
		status["webhookOnline"] = snapshot.WebhookOnline
	}
	if !snapshot.LastPoll.IsZero() {
		status["lastPoll"] = snapshot.LastPoll
	}
	if !snapshot.NextPoll.IsZero() {
		status["nextPoll"] = snapshot.NextPoll
	}
	ui.mu.RLock()
	settings := ui.settings
	ui.mu.RUnlock()
	if settings == nil {
		status["settingsError"] = "settings unavailable"
	} else if current, err := settings(ctx, core.SettingsUpdate{}); err != nil {
		status["settingsError"] = err.Error()
	} else {
		status["settings"] = current
	}
	ui.mu.RLock()
	hasAgents, probed := ui.agents != nil, ui.agentsProbed
	statuses := append([]core.AgentStatus(nil), ui.agentStatuses...)
	ui.mu.RUnlock()
	switch {
	case !hasAgents:
		status["agentsError"] = "agents unavailable"
	case !probed:
		ui.refreshAgentStatuses()
		status["agentsError"] = "agents are still being probed; ask again shortly"
	default:
		status["agents"] = statuses
	}
	return status, nil
}

func (ui *Server) mcpListJobs(_ context.Context, args mcpArguments) (any, error) {
	jobs := []map[string]any{}
	for _, job := range ui.mcpState().Snapshot.Jobs {
		if args.Status != "" && job.Status != args.Status {
			continue
		}
		jobs = append(jobs, mcpJobSummary(job))
	}
	return map[string]any{"jobs": jobs}, nil
}

func (ui *Server) mcpGetJob(_ context.Context, args mcpArguments) (any, error) {
	job, err := ui.mcpFindJob(args)
	if err != nil {
		return nil, err
	}
	lines := mcpLogLines
	if args.Lines != nil {
		lines = *args.Lines
	}
	detail := mcpJobSummary(job)
	detail["checkoutDirectory"] = job.CheckoutDirectory
	detail["sessionID"] = job.SessionID
	detail["effort"] = job.Effort
	log := job.Log
	if lines > 0 {
		log = lastLogLines(log, lines)
	}
	detail["scrollback"] = log
	return detail, nil
}

func (ui *Server) mcpExplainJob(_ context.Context, args mcpArguments) (any, error) {
	state := ui.mcpState()
	job, err := findJob(state.Snapshot.Jobs, args.Target, args.Number)
	if err != nil {
		return nil, err
	}
	detail := mcpJobSummary(job)
	detail["explanation"] = explainJob(job, state.Snapshot)
	detail["lastOutput"] = lastLogLines(job.Log, mcpExplainLines)
	return detail, nil
}

// explainJob says in words why a job is where it is and what moves it on.
func explainJob(job core.JobSnapshot, snapshot core.Snapshot) string {
	switch job.Status {
	case "active":
		text := "Its agent is running"
		if !job.Started.IsZero() {
			text += fmt.Sprintf(", started %s ago", core.FormatInterval(time.Since(job.Started).Truncate(time.Second)))
		}
		return text + ". If its last output has not changed for a while it may be stuck; the stuck-job watchdog covers a silent job, and trigger_unblock runs a round for it now."
	case "waiting":
		text := "Its agent scheduled a wakeup and is waiting for it"
		if !job.WakeAt.IsZero() {
			text += fmt.Sprintf(", due %s", job.WakeAt.Format(time.RFC3339))
		}
		if job.WaitReason != "" {
			text += ": " + job.WaitReason
		}
		return text + "."
	case "paused":
		if job.WaitReason == core.ManualPauseReason {
			return "The owner paused it. Nothing wakes it on its own, and the stuck-job watchdog leaves it alone: resume_job (Retry in the dashboard) requeues it, and stop_job clears it."
		}
		text := "It is paused, holding no agent slot"
		if job.WaitReason != "" {
			text += ": " + job.WaitReason
		}
		return text + ". glorp wakes it on its own once that changes on GitHub. The stuck-job watchdog covers it, trigger_unblock runs a round for it now, and resume_job clears the pause and requeues it at once."
	case "queued":
		return fmt.Sprintf("It is queued for an agent slot; %d of %d slots are running.", snapshot.Running, snapshot.Concurrency)
	case "stopping":
		return "It was stopped and glorp is waiting for its agent to exit."
	}
	if job.WaitReason != "" {
		return fmt.Sprintf("Its status is %s: %s.", job.Status, job.WaitReason)
	}
	return fmt.Sprintf("Its status is %s.", job.Status)
}

// mcpJobAction performs a job action exactly as the dashboard's button does,
// refusing what the button would be disabled for. Resume is Retry on a paused
// job, which clears the pause (issue #763).
func (ui *Server) mcpJobAction(ctx context.Context, args mcpArguments, action string) (any, error) {
	job, err := ui.mcpFindJob(args)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s#%d", job.Target, job.Number)
	available := core.JobActionAvailability(job.Status)
	switch action {
	case "resume":
		if job.Status != "paused" {
			return nil, fmt.Errorf("%s is %s, not paused; there is nothing to resume", key, job.Status)
		}
		action = "retry"
	case "retry":
		if !available.Retry {
			return nil, fmt.Errorf("%s cannot be retried: %s", key, available.RetryReason)
		}
	case "pause":
		if !available.Pause {
			return nil, fmt.Errorf("%s cannot be paused while %s", key, job.Status)
		}
	case "stop":
		if !available.Stop {
			return nil, fmt.Errorf("%s cannot be stopped while %s", key, job.Status)
		}
	}
	ui.mu.RLock()
	handler := ui.action
	ui.mu.RUnlock()
	if handler == nil {
		return nil, errors.New("job actions unavailable")
	}
	if err := handler(ctx, core.JobAction{Action: action, Target: job.Target, Number: job.Number}); err != nil {
		if errors.Is(err, core.ErrNotReady) {
			return nil, errors.New("job actions unavailable: the run is still starting; try again")
		}
		return nil, err
	}
	result := map[string]any{"ok": true, "action": action}
	if current, err := findJob(ui.mcpState().Snapshot.Jobs, job.Target, job.Number); err == nil {
		result["job"] = mcpJobSummary(current)
	} else {
		result["job"] = nil
	}
	return result, nil
}

func (ui *Server) mcpTriggerUnblock(ctx context.Context, args mcpArguments) (any, error) {
	ui.mu.RLock()
	handler := ui.unblock
	ui.mu.RUnlock()
	if handler == nil {
		return nil, errors.New("unblock rounds unavailable")
	}
	if args.Target != "" && args.Number <= 0 {
		return nil, errors.New("a target needs a job number")
	}
	result, err := handler(ctx, core.UnblockRequest{Target: args.Target, Number: args.Number})
	if err != nil {
		if errors.Is(err, core.ErrNotReady) {
			return nil, errors.New("unblock rounds unavailable: the run is still starting; try again")
		}
		return nil, err
	}
	if result.Jobs == nil {
		result.Jobs = []string{}
	}
	return map[string]any{"jobs": result.Jobs, "message": result.Message}, nil
}

func (ui *Server) mcpUpdateIssue(ctx context.Context, args mcpArguments) (any, error) {
	ui.mu.RLock()
	handler := ui.issueUpdate
	ui.mu.RUnlock()
	if handler == nil {
		return nil, errors.New("issue updates unavailable")
	}
	if args.Number <= 0 {
		return nil, errors.New("an issue or pull request number is required")
	}
	if strings.TrimSpace(args.Text) == "" {
		return nil, errors.New("the update text is empty")
	}
	repository := args.Repository
	if repository == "" {
		resolved, err := updateRepository(ui.mcpState().Snapshot, args.Number)
		if err != nil {
			return nil, err
		}
		repository = resolved
	}
	result, err := handler(ctx, core.IssueUpdate{Repository: repository, Number: args.Number, Text: args.Text})
	if err != nil {
		return nil, err
	}
	return map[string]any{"url": result.URL, "author": result.Author, "repository": repository, "number": args.Number}, nil
}

// updateRepository picks the repository an update without one goes to: that
// of the one job with the number, or else the instance's only watched
// repository.
func updateRepository(snapshot core.Snapshot, number int) (string, error) {
	var found []string
	for _, job := range snapshot.Jobs {
		if job.Number != number {
			continue
		}
		repository := job.Repository
		if repository == "" && core.ValidRepo(job.Target) {
			repository = job.Target
		}
		if repository != "" && !slices.Contains(found, repository) {
			found = append(found, repository)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return "", fmt.Errorf("jobs in %s all have #%d; name the repository", strings.Join(found, " and "), number)
	}
	var repositories []string
	for _, target := range snapshot.Targets {
		if core.ValidRepo(target) {
			repositories = append(repositories, target)
		}
	}
	if len(repositories) == 1 {
		return repositories[0], nil
	}
	return "", fmt.Errorf("name the repository of #%d", number)
}

func (ui *Server) mcpFindJob(args mcpArguments) (core.JobSnapshot, error) {
	return findJob(ui.mcpState().Snapshot.Jobs, args.Target, args.Number)
}

// findJob picks the job number names, in target when given.
func findJob(jobs []core.JobSnapshot, target string, number int) (core.JobSnapshot, error) {
	if number <= 0 {
		return core.JobSnapshot{}, errors.New("a job number is required")
	}
	var matches []core.JobSnapshot
	for _, job := range jobs {
		if job.Number == number && (target == "" || job.Target == target) {
			matches = append(matches, job)
		}
	}
	switch {
	case len(matches) == 1:
		return matches[0], nil
	case len(matches) > 1:
		return core.JobSnapshot{}, fmt.Errorf("more than one target has job #%d; name its target", number)
	case target != "":
		return core.JobSnapshot{}, fmt.Errorf("no job %s#%d", target, number)
	}
	return core.JobSnapshot{}, fmt.Errorf("no job #%d", number)
}

// mcpJobSummary is a job as list_jobs reports it: its state and the actions
// it accepts, without its scrollback.
func mcpJobSummary(job core.JobSnapshot) map[string]any {
	actions := core.JobActionAvailability(job.Status)
	summary := map[string]any{
		"key":    fmt.Sprintf("%s#%d", job.Target, job.Number),
		"target": job.Target,
		"number": job.Number,
		"title":  job.Title,
		"status": job.Status,
		"agent":  job.Agent,
		"model":  job.Model,
		"actions": map[string]any{
			"retry":  actions.Retry,
			"pause":  actions.Pause,
			"resume": job.Status == "paused",
			"stop":   actions.Stop,
		},
	}
	if !actions.Retry {
		summary["retryUnavailable"] = actions.RetryReason
	}
	if job.Repository != "" {
		summary["repository"] = job.Repository
	}
	if url := job.IssueURL(); url != "" {
		summary["issueURL"] = url
	}
	if job.PullRequest > 0 {
		summary["pullRequest"] = job.PullRequest
		summary["pullRequestURL"] = job.PullRequestURL()
	}
	if job.HasProgress() {
		summary["progress"] = map[string]int{"done": job.TasksDone, "total": job.TasksTotal, "percent": job.Progress}
	}
	if !job.Started.IsZero() {
		summary["started"] = job.Started
	}
	if job.WaitReason != "" {
		summary["waitReason"] = job.WaitReason
	}
	if !job.WakeAt.IsZero() {
		summary["wakeAt"] = job.WakeAt
	}
	return summary
}

// lastLogLines returns the last n lines of a scrollback.
func lastLogLines(log string, n int) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(log, "\r\n", "\n"), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func lastItems(items []string, n int) []string {
	if len(items) > n {
		items = items[len(items)-n:]
	}
	if items == nil {
		return []string{}
	}
	return items
}
