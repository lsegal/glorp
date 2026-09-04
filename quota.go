package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lsegal/glorp/agents"
	"github.com/lsegal/glorp/process"
)

type codexPrimaryRateLimit struct {
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
}

type codexRateLimitResponse struct {
	RateLimits struct {
		Primary *codexPrimaryRateLimit `json:"primary"`
	} `json:"rateLimits"`
}

type codexQuotaReader struct {
	Binary string
	mu     sync.Mutex
	quota  string
	err    error
	readAt time.Time
}

func (r *codexQuotaReader) Read(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.readAt) < time.Minute {
		return r.quota, r.err
	}
	quota, err := readCodexQuota(ctx, r.Binary)
	if err == nil {
		r.quota = quota
	}
	r.err = err
	r.readAt = time.Now()
	return r.quota, r.err
}

// codexQuotaArgv is the argv the Codex quota reader runs. `app-server` speaks
// JSON-RPC over stdio by default; it once took an explicit `--stdio` flag,
// which current releases reject outright, taking the whole reading with it.
// The transport is the default, so naming it is what breaks, not what works.
func codexQuotaArgv(binary string) []string {
	return []string{binary, "app-server"}
}

func readCodexQuota(ctx context.Context, binary string) (string, error) {
	argv := codexQuotaArgv(binary)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := process.Start(cmd); err != nil {
		return "", err
	}
	defer func() { _ = process.Stop(cmd) }()
	for _, request := range codexQuotaRequests() {
		if _, err := fmt.Fprintln(stdin, request); err != nil {
			return "", err
		}
	}
	// Keep stdin open while reading: app-server can finish the request only
	// after it has sent the response, and treats EOF as a client disconnect.
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var message struct {
			ID     int                     `json:"id"`
			Result *codexRateLimitResponse `json:"result"`
			Error  json.RawMessage         `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.ID != 2 {
			continue
		}
		if message.Result == nil {
			return "", fmt.Errorf("codex rate limit request failed: %s", message.Error)
		}
		return formatCodexQuota(message.Result.RateLimits.Primary), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("codex rate limit response not received")
}

func codexQuotaRequests() []string {
	return []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"glorp","title":"glorp","version":"dev"}}}`,
		`{"method":"initialized","params":{}}`,
		`{"id":2,"method":"account/rateLimits/read"}`,
	}
}

func formatCodexQuota(primary *codexPrimaryRateLimit) string {
	if primary == nil {
		return ""
	}
	remaining := max(0, 100-primary.UsedPercent)
	window := "quota"
	if primary.WindowDurationMins != nil && *primary.WindowDurationMins >= 7*24*60 {
		window = "weekly"
	}
	return fmt.Sprintf("%s %d%% left", window, remaining)
}

type claudeQuotaReader struct {
	Binary string
	mu     sync.Mutex
	quota  string
	err    error
	readAt time.Time
}

func (r *claudeQuotaReader) Read(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.readAt) < time.Minute {
		return r.quota, r.err
	}
	quota, err := readClaudeQuota(ctx, r.Binary)
	if err == nil {
		r.quota = quota
	}
	r.err = err
	r.readAt = time.Now()
	return r.quota, r.err
}

// readClaudeQuota runs the local `/usage` slash command, which reports the
// account's current session/week usage without making a billed API request
// (unlike a normal prompt, it costs no tokens and no money).
func readClaudeQuota(ctx context.Context, binary string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, "--print", "--output-format=json")
	cmd.Stdin = strings.NewReader(claudeQuotaRequest())
	out, err := process.Output(cmd)
	if err != nil {
		return "", err
	}
	var response struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return "", err
	}
	quota := formatClaudeQuota(response.Result)
	if quota == "" {
		return "", fmt.Errorf("claude usage response missing session percentage")
	}
	return quota, nil
}

// claudeQuotaRequest is the "/usage" slash command: a local status report
// handled by the CLI itself, so unlike a normal prompt it costs no tokens
// and makes no billed API request.
func claudeQuotaRequest() string {
	return "/usage"
}

var (
	claudeSessionUsagePattern = regexp.MustCompile(`Current session:\s*(\d+)% used`)
	claudeWeekUsagePattern    = regexp.MustCompile(`Current week \(all models\):\s*(\d+)% used`)
)

func formatClaudeQuota(usageText string) string {
	session := claudeSessionUsagePattern.FindStringSubmatch(usageText)
	if session == nil {
		return ""
	}
	sessionUsed, err := strconv.Atoi(session[1])
	if err != nil {
		return ""
	}
	parts := []string{fmt.Sprintf("session %d%% left", max(0, 100-sessionUsed))}
	if week := claudeWeekUsagePattern.FindStringSubmatch(usageText); week != nil {
		if weekUsed, err := strconv.Atoi(week[1]); err == nil {
			parts = append(parts, fmt.Sprintf("week %d%% left", max(0, 100-weekUsed)))
		}
	}
	return strings.Join(parts, ", ")
}

// quotaCommandWaitDelay is how long a cancelled quota command is given to let
// go of its output pipe before the read gives up on it.
const quotaCommandWaitDelay = time.Second

// commandQuotaReader is the generic quota source: it runs the argv a
// definition names, reads the JSON that argv prints, and renders the fields
// the definition points at into the definition's own template. It is what
// lets an agent glorp has never heard of report a quota, and it caches on the
// same one-minute terms as the built-in readers so a poll never re-runs it.
type commandQuotaReader struct {
	Binary string
	Spec   agents.Quota
	mu     sync.Mutex
	quota  string
	err    error
	readAt time.Time
}

func (r *commandQuotaReader) Read(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.readAt) < time.Minute {
		return r.quota, r.err
	}
	quota, err := readCommandQuota(ctx, r.Binary, r.Spec)
	if err == nil {
		r.quota = quota
	}
	r.err = err
	r.readAt = time.Now()
	return r.quota, r.err
}

// readCommandQuota runs one quota command under the definition's timeout. A
// quota is a status-bar nicety, so a command that hangs is abandoned rather
// than allowed to hold up the poll that asked for it.
func readCommandQuota(ctx context.Context, binary string, spec agents.Quota) (string, error) {
	argv := spec.Argv(binary)
	if len(argv) == 0 {
		return "", fmt.Errorf("quota command is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Cancelling kills the command, but a grandchild it left holding the
	// output pipe would keep the read waiting on a process that is already
	// gone. WaitDelay closes the pipe behind it so the timeout is the timeout.
	cmd.WaitDelay = quotaCommandWaitDelay
	out, err := process.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("run quota command %s: %w", argv[0], err)
	}
	var document any
	if err := json.Unmarshal(out, &document); err != nil {
		return "", fmt.Errorf("quota command %s did not print JSON: %w", argv[0], err)
	}
	return formatCommandQuota(spec, document)
}

// formatCommandQuota renders the definition's template from the decoded
// document. A field the template asks for and the document does not have is
// an error rather than an empty substitution, so a changed output format
// leaves the last good reading up instead of replacing it with nonsense.
func formatCommandQuota(spec agents.Quota, document any) (string, error) {
	text := spec.FormatTemplate()
	if strings.Contains(text, "{percent") {
		used, ok := quotaNumberAt(document, spec.PercentUsed)
		if !ok {
			return "", fmt.Errorf("quota field %q is missing or not a number", spec.PercentUsed)
		}
		rounded := int(math.Round(used))
		text = strings.ReplaceAll(text, "{percentUsed}", strconv.Itoa(rounded))
		text = strings.ReplaceAll(text, "{percentLeft}", strconv.Itoa(min(100, max(0, 100-rounded))))
	}
	if strings.Contains(text, "{resetAt}") {
		reset, ok := quotaStringAt(document, spec.ResetAt)
		if !ok {
			return "", fmt.Errorf("quota field %q is missing", spec.ResetAt)
		}
		text = strings.ReplaceAll(text, "{resetAt}", reset)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("quota command produced an empty reading")
	}
	return text, nil
}

// quotaValueAt walks a dotted path through the decoded JSON document. Only
// object keys are traversed: a definition points at a named field, not into
// an array, which keeps the path syntax something a config file can be read
// aloud from.
func quotaValueAt(document any, path string) (any, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	value := document
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if value, ok = object[key]; !ok {
			return nil, false
		}
	}
	return value, true
}

// quotaNumberAt reads a numeric field, accepting a JSON string that holds a
// number so a CLI that quotes its percentages still works.
func quotaNumberAt(document any, path string) (float64, bool) {
	value, ok := quotaValueAt(document, path)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(typed), "%"), 64)
		return number, err == nil
	}
	return 0, false
}

// quotaStringAt reads a text field, rendering a number as one so a reset time
// expressed as a Unix timestamp is still printable.
func quotaStringAt(document any, path string) (string, bool) {
	value, ok := quotaValueAt(document, path)
	if !ok {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		return text, text != ""
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	}
	return "", false
}

// namedQuotaReader reads quota text for a single named agent. The read
// reports why it could not answer as well as what it read, so a reader whose
// command fails is something a caller can show rather than an empty string
// indistinguishable from an agent that tracks no quota at all.
type namedQuotaReader struct {
	name string
	read func(context.Context) (string, error)
}

// namedQuotaReaders builds one quota reader per configured agent, deduplicated
// by name. Which reader an agent gets comes from its definition's quota block
// rather than from its name, so a config-defined agent can report a quota
// without a code change. An agent whose definition names no quota source --
// the default, and what an agent the registry does not know falls back to --
// still gets an entry so the UI can show that its quota is not tracked, but
// that entry never spawns a process.
func namedQuotaReaders(registry *agents.Registry, names []string, binaryFor func(string) string) []namedQuotaReader {
	seen := make(map[string]bool, len(names))
	readers := make([]namedQuotaReader, 0, len(names))
	untracked := func(context.Context) (string, error) { return "", nil }
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		definition, ok := registry.Lookup(name)
		if !ok {
			readers = append(readers, namedQuotaReader{name: name, read: untracked})
			continue
		}
		var read func(context.Context) (string, error)
		switch definition.Quota.ReaderName() {
		case agents.QuotaCodex:
			read = (&codexQuotaReader{Binary: binaryFor(name)}).Read
		case agents.QuotaClaude:
			read = (&claudeQuotaReader{Binary: binaryFor(name)}).Read
		case agents.QuotaCommand:
			read = (&commandQuotaReader{Binary: binaryFor(name), Spec: definition.Quota}).Read
		default:
			read = untracked
		}
		readers = append(readers, namedQuotaReader{name: name, read: read})
	}
	return readers
}

// combinedQuotaReader reads every named reader's quota and returns the
// results keyed by agent name.
func combinedQuotaReader(readers []namedQuotaReader) func(context.Context) map[string]string {
	return func(ctx context.Context) map[string]string {
		result := make(map[string]string, len(readers))
		for _, reader := range readers {
			quota, _ := reader.read(ctx)
			result[reader.name] = quota
		}
		return result
	}
}

func formatNamedQuotas(quotas map[string]string) string {
	names := make([]string, 0, len(quotas))
	for name := range quotas {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		text := strings.TrimSpace(quotas[name])
		if text == "" {
			text = "not tracked"
		}
		parts[i] = name + ": " + text
	}
	return strings.Join(parts, ", ")
}

func quotaText(snapshot GlorpSnapshot) string {
	if len(snapshot.Quotas) > 0 {
		return "quota: " + formatNamedQuotas(snapshot.Quotas)
	}
	if strings.TrimSpace(snapshot.Quota) != "" {
		return "quota: " + snapshot.Quota
	}
	if snapshot.TokenLimit > 0 {
		return fmt.Sprintf("tokens: %d/%d", snapshot.TokensUsed, snapshot.TokenLimit)
	}
	return "quota: unavailable"
}
