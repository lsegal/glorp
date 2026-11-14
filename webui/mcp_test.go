package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/core"
)

// stubInstance is a dashboard server wired to stub handlers that record what
// the MCP tools asked the instance to do.
type stubInstance struct {
	server   *Server
	http     *httptest.Server
	actions  []core.JobAction
	unblocks []core.UnblockRequest
	updates  []core.IssueUpdate
}

func newStubInstance(t *testing.T) *stubInstance {
	t.Helper()
	server, err := New("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubInstance{server: server}
	server.Snapshot(core.Snapshot{
		Identity:    "ABCD1234",
		Targets:     []string{"o/r"},
		Running:     1,
		Paused:      2,
		Queued:      1,
		Concurrency: 2,
		Interval:    20 * time.Second,
		Quota:       "claude: week 78% left",
		Jobs: []core.JobSnapshot{
			{Target: "o/r", Number: 1, Title: "Busy", Status: "active", Agent: "claude", Model: "opus", SessionID: "s-1", CheckoutDirectory: "/tmp/r-1", Log: "line 1\nline 2\nline 3\n", Started: time.Now().Add(-time.Minute)},
			{Target: "o/r", Number: 2, Title: "Blocked", Status: "paused", WaitReason: "waits on #1 to close", PullRequest: 12, Log: "parked\n"},
			{Target: "o/r", Number: 3, Title: "Held", Status: "paused", WaitReason: core.ManualPauseReason},
			{Target: "o/r", Number: 4, Title: "Next", Status: "queued"},
		},
		VisibleJobs: 1,
	})
	server.Log("watching o/r")
	server.SetJobActionHandler(func(_ context.Context, action core.JobAction) error {
		stub.actions = append(stub.actions, action)
		return nil
	})
	server.SetSettingsHandler(func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error) {
		return core.SettingsSnapshot{Concurrency: 2, UnblockInterval: "15m"}, nil
	})
	server.SetUnblockHandler(func(_ context.Context, request core.UnblockRequest) (core.UnblockResult, error) {
		stub.unblocks = append(stub.unblocks, request)
		return core.UnblockResult{Jobs: []string{"o/r#2"}, Message: "running an unblock round for 1 job(s)"}, nil
	})
	server.SetIssueUpdateHandler(func(_ context.Context, update core.IssueUpdate) (core.IssueUpdateResult, error) {
		stub.updates = append(stub.updates, update)
		return core.IssueUpdateResult{URL: "https://github.com/o/r/issues/2#issuecomment-9", Author: "octo"}, nil
	})
	stub.http = httptest.NewServer(server)
	t.Cleanup(stub.http.Close)
	return stub
}

// rpc posts one JSON-RPC message to /mcp and decodes the response.
func (s *stubInstance) rpc(t *testing.T, method string, params any) mcpResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	request, _ := http.NewRequest(http.MethodPost, s.http.URL+MCPPath, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d", method, response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("%s answered with content type %q", method, got)
	}
	var decoded mcpResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.ID) != "7" || decoded.JSONRPC != "2.0" {
		t.Fatalf("%s answered with id %s, jsonrpc %q", method, decoded.ID, decoded.JSONRPC)
	}
	return decoded
}

// call runs a tool and returns its result text and whether it is an error.
func (s *stubInstance) call(t *testing.T, name string, args map[string]any) (string, bool) {
	t.Helper()
	response := s.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})
	if response.Error != nil {
		t.Fatalf("%s failed: %+v", name, response.Error)
	}
	raw, _ := json.Marshal(response.Result)
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("%s returned %s", name, raw)
	}
	return result.Content[0].Text, result.IsError
}

func (s *stubInstance) callJSON(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	text, isError := s.call(t, name, args)
	if isError {
		t.Fatalf("%s returned an error: %s", name, text)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("%s returned %q: %v", name, text, err)
	}
	return value
}

func TestMCPHandshake(t *testing.T) {
	stub := newStubInstance(t)
	response := stub.rpc(t, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	result, _ := response.Result.(map[string]any)
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize did not agree on the client's version: %v", result)
	}
	if info, _ := result["serverInfo"].(map[string]any); info["name"] != "glorp" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", result["serverInfo"])
	}
	if capabilities, _ := result["capabilities"].(map[string]any); capabilities["tools"] == nil {
		t.Errorf("capabilities do not offer tools: %v", result["capabilities"])
	}
	unknown := stub.rpc(t, "initialize", map[string]any{"protocolVersion": "1999-01-01"})
	if got := unknown.Result.(map[string]any)["protocolVersion"]; got != mcpProtocolVersions[0] {
		t.Errorf("an unknown client version got %v, want the newest", got)
	}
	// The initialized notification is accepted with no body.
	response2, err := http.Post(stub.http.URL+MCPPath, "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	response2.Body.Close()
	if response2.StatusCode != http.StatusAccepted {
		t.Errorf("notification answered %d, want 202", response2.StatusCode)
	}
	if ping := stub.rpc(t, "ping", nil); ping.Error != nil {
		t.Errorf("ping failed: %+v", ping.Error)
	}
	if missing := stub.rpc(t, "resources/list", nil); missing.Error == nil || missing.Error.Code != -32601 {
		t.Errorf("an unknown method answered %+v", missing)
	}
}

func TestMCPTransportRefusesGetAndForeignOrigins(t *testing.T) {
	stub := newStubInstance(t)
	response, err := http.Get(stub.http.URL + MCPPath)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET answered %d, want 405", response.StatusCode)
	}
	for origin, want := range map[string]int{"https://evil.example": http.StatusForbidden, "http://localhost:5173": http.StatusOK, stub.http.URL: http.StatusOK} {
		request, _ := http.NewRequest(http.MethodPost, stub.http.URL+MCPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		request.Header.Set("Origin", origin)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("origin %s answered %d, want %d", origin, response.StatusCode, want)
		}
	}
	bad, err := http.Post(stub.http.URL+MCPPath, "application/json", strings.NewReader(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	var decoded mcpResponse
	_ = json.NewDecoder(bad.Body).Decode(&decoded)
	if decoded.Error == nil || decoded.Error.Code != -32700 {
		t.Errorf("malformed JSON answered %+v", decoded)
	}
}

func TestMCPListsEveryTool(t *testing.T) {
	stub := newStubInstance(t)
	response := stub.rpc(t, "tools/list", map[string]any{})
	raw, _ := json.Marshal(response.Result)
	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema["type"] != "object" {
			t.Errorf("tool %s has no description or object schema: %+v", tool.Name, tool)
		}
		if _, ok := mcpToolHandlers[tool.Name]; !ok {
			t.Errorf("tool %s is listed but has no handler", tool.Name)
		}
	}
	for _, want := range []string{"get_status", "list_jobs", "get_job", "explain_job", "retry_job", "pause_job", "resume_job", "stop_job", "trigger_unblock", "update_issue"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools/list is missing %s: %v", want, names)
		}
	}
	if len(names) != len(mcpToolHandlers) {
		t.Errorf("listed %d tools for %d handlers", len(names), len(mcpToolHandlers))
	}
	if call := stub.rpc(t, "tools/call", map[string]any{"name": "no_such_tool"}); call.Error == nil || call.Error.Code != -32602 {
		t.Errorf("an unknown tool answered %+v", call)
	}
}

func TestMCPStatusReportsTheLiveInstance(t *testing.T) {
	stub := newStubInstance(t)
	status := stub.callJSON(t, "get_status", nil)
	if status["version"] != "1.2.3" || status["identity"] != "ABCD1234" || status["quota"] != "claude: week 78% left" {
		t.Errorf("status = %v", status)
	}
	if jobs, _ := status["jobs"].(map[string]any); jobs["running"] != float64(1) || jobs["paused"] != float64(2) {
		t.Errorf("job counts = %v", status["jobs"])
	}
	if settings, _ := status["settings"].(map[string]any); settings["unblockInterval"] != "15m" {
		t.Errorf("settings = %v", status["settings"])
	}
	if logs, _ := status["recentLogs"].([]any); len(logs) != 1 || logs[0] != "watching o/r" {
		t.Errorf("recentLogs = %v", status["recentLogs"])
	}
	if status["agentsError"] != "agents unavailable" {
		t.Errorf("a server with no agents handler reported %v", status["agentsError"])
	}
	// A later snapshot is what the next call reports.
	stub.server.Snapshot(core.Snapshot{Identity: "ABCD1234", Running: 0})
	if jobs, _ := stub.callJSON(t, "get_status", nil)["jobs"].(map[string]any); jobs["running"] != float64(0) {
		t.Errorf("status did not follow the live snapshot: %v", jobs)
	}
}

func TestMCPListsJobsWithoutScrollback(t *testing.T) {
	stub := newStubInstance(t)
	jobs, _ := stub.callJSON(t, "list_jobs", nil)["jobs"].([]any)
	if len(jobs) != 4 {
		t.Fatalf("listed %d jobs, want 4: %v", len(jobs), jobs)
	}
	first, _ := jobs[0].(map[string]any)
	if first["key"] != "o/r#1" || first["status"] != "active" || first["scrollback"] != nil {
		t.Errorf("first job = %v", first)
	}
	if actions, _ := first["actions"].(map[string]any); actions["stop"] != true || actions["resume"] != false {
		t.Errorf("active job's actions = %v", first["actions"])
	}
	paused, _ := stub.callJSON(t, "list_jobs", map[string]any{"status": "paused"})["jobs"].([]any)
	if len(paused) != 2 {
		t.Errorf("status filter listed %v", paused)
	}
}

func TestMCPGetJobReturnsSessionAndScrollback(t *testing.T) {
	stub := newStubInstance(t)
	job := stub.callJSON(t, "get_job", map[string]any{"number": 1, "lines": 2})
	if job["sessionID"] != "s-1" || job["checkoutDirectory"] != "/tmp/r-1" || job["scrollback"] != "line 2\nline 3" {
		t.Errorf("job = %v", job)
	}
	if all := stub.callJSON(t, "get_job", map[string]any{"number": 1, "lines": 0}); all["scrollback"] != "line 1\nline 2\nline 3\n" {
		t.Errorf("lines 0 returned %q", all["scrollback"])
	}
	// Job 2 is beyond the dashboard's first page, whose jobs alone carry
	// their Log in /api/state; get_job still reads its transcript.
	if second := stub.callJSON(t, "get_job", map[string]any{"number": 2}); second["scrollback"] != "parked" {
		t.Errorf("a job off the first page returned %q", second["scrollback"])
	}
	if text, isError := stub.call(t, "get_job", map[string]any{"number": 99}); !isError || !strings.Contains(text, "no job #99") {
		t.Errorf("a missing job returned %q", text)
	}
	if text, isError := stub.call(t, "get_job", map[string]any{"number": 1, "bogus": true}); !isError || !strings.Contains(text, "invalid arguments") {
		t.Errorf("an unknown argument returned %q", text)
	}
}

func TestMCPExplainJobSaysWhyItIsStuck(t *testing.T) {
	stub := newStubInstance(t)
	for number, want := range map[int]string{
		1: "agent is running",
		2: "paused, holding no agent slot: waits on #1 to close",
		3: "owner paused it",
		4: "queued for an agent slot; 1 of 2 slots are running",
	} {
		explained := stub.callJSON(t, "explain_job", map[string]any{"number": number})
		if text, _ := explained["explanation"].(string); !strings.Contains(text, want) {
			t.Errorf("job %d explanation = %q, want %q", number, text, want)
		}
	}
	if last := stub.callJSON(t, "explain_job", map[string]any{"number": 1})["lastOutput"]; last != "line 1\nline 2\nline 3" {
		t.Errorf("lastOutput = %q", last)
	}
}

func TestMCPJobActionsMatchTheDashboardButtons(t *testing.T) {
	stub := newStubInstance(t)
	for _, call := range []struct {
		tool   string
		number int
		want   string
	}{
		{"pause_job", 1, "pause"},
		{"stop_job", 1, "stop"},
		{"retry_job", 2, "retry"},
		{"resume_job", 3, "retry"},
		{"pause_job", 4, "pause"},
	} {
		result := stub.callJSON(t, call.tool, map[string]any{"number": call.number, "target": "o/r"})
		if result["ok"] != true || result["action"] != call.want {
			t.Errorf("%s on #%d = %v", call.tool, call.number, result)
		}
	}
	want := []core.JobAction{{Action: "pause", Target: "o/r", Number: 1}, {Action: "stop", Target: "o/r", Number: 1}, {Action: "retry", Target: "o/r", Number: 2}, {Action: "retry", Target: "o/r", Number: 3}, {Action: "pause", Target: "o/r", Number: 4}}
	if !slices.Equal(stub.actions, want) {
		t.Errorf("actions = %+v, want %+v", stub.actions, want)
	}
	// What the dashboard disables, the tools refuse without asking the run.
	for _, call := range []struct {
		tool   string
		number int
		want   string
	}{
		{"retry_job", 4, "Already queued"},
		{"resume_job", 1, "not paused"},
		{"stop_job", 4, "cannot be stopped while queued"},
		{"pause_job", 2, "cannot be paused while paused"},
	} {
		text, isError := stub.call(t, call.tool, map[string]any{"number": call.number})
		if !isError || !strings.Contains(text, call.want) {
			t.Errorf("%s on #%d returned %q, want an error with %q", call.tool, call.number, text, call.want)
		}
	}
	if len(stub.actions) != len(want) {
		t.Errorf("refused actions reached the run: %+v", stub.actions[len(want):])
	}
	stub.server.SetJobActionHandler(func(context.Context, core.JobAction) error { return errors.New("job cannot be requeued") })
	if text, isError := stub.call(t, "retry_job", map[string]any{"number": 2}); !isError || text != "job cannot be requeued" {
		t.Errorf("a failed action returned %q", text)
	}
}

func TestMCPTriggerUnblockRunsARound(t *testing.T) {
	stub := newStubInstance(t)
	result := stub.callJSON(t, "trigger_unblock", nil)
	if jobs, _ := result["jobs"].([]any); len(jobs) != 1 || jobs[0] != "o/r#2" {
		t.Errorf("result = %v", result)
	}
	stub.callJSON(t, "trigger_unblock", map[string]any{"number": 2, "target": "o/r"})
	want := []core.UnblockRequest{{}, {Target: "o/r", Number: 2}}
	if !slices.Equal(stub.unblocks, want) {
		t.Errorf("unblock requests = %+v, want %+v", stub.unblocks, want)
	}
	stub.server.SetUnblockHandler(func(context.Context, core.UnblockRequest) (core.UnblockResult, error) {
		return core.UnblockResult{}, core.ErrNotReady
	})
	if text, isError := stub.call(t, "trigger_unblock", nil); !isError || !strings.Contains(text, "still starting") {
		t.Errorf("a run still starting returned %q", text)
	}
}

func TestMCPUpdateIssuePostsAComment(t *testing.T) {
	stub := newStubInstance(t)
	result := stub.callJSON(t, "update_issue", map[string]any{"number": 2, "text": "The blocker merged; rebase."})
	if result["url"] != "https://github.com/o/r/issues/2#issuecomment-9" || result["author"] != "octo" || result["repository"] != "o/r" {
		t.Errorf("result = %v", result)
	}
	stub.callJSON(t, "update_issue", map[string]any{"number": 7, "repository": "other/repo", "text": "hi"})
	want := []core.IssueUpdate{{Repository: "o/r", Number: 2, Text: "The blocker merged; rebase."}, {Repository: "other/repo", Number: 7, Text: "hi"}}
	if !slices.Equal(stub.updates, want) {
		t.Errorf("updates = %+v, want %+v", stub.updates, want)
	}
	if text, isError := stub.call(t, "update_issue", map[string]any{"number": 2, "text": "  "}); !isError || !strings.Contains(text, "empty") {
		t.Errorf("an empty update returned %q", text)
	}
	stub.server.Snapshot(core.Snapshot{Targets: []string{"a/b", "c/d"}})
	if text, isError := stub.call(t, "update_issue", map[string]any{"number": 5, "text": "x"}); !isError || !strings.Contains(text, "name the repository") {
		t.Errorf("an ambiguous repository returned %q", text)
	}
}
