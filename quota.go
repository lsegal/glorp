package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	readAt time.Time
}

func (r *codexQuotaReader) Read(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.readAt) < time.Minute {
		return r.quota
	}
	quota, err := readCodexQuota(ctx, r.Binary)
	if err == nil {
		r.quota = quota
	}
	r.readAt = time.Now()
	return r.quota
}

func readCodexQuota(ctx context.Context, binary string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
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
	readAt time.Time
}

func (r *claudeQuotaReader) Read(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.readAt) < time.Minute {
		return r.quota
	}
	quota, err := readClaudeQuota(ctx, r.Binary)
	if err == nil {
		r.quota = quota
	}
	r.readAt = time.Now()
	return r.quota
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

// namedQuotaReader reads quota text for a single named agent.
type namedQuotaReader struct {
	name string
	read func(context.Context) string
}

// namedQuotaReaders builds one quota reader per configured agent, deduplicated
// by name. Agents without a known quota source still get an entry so the UI
// can show that their quota is not tracked.
func namedQuotaReaders(agents []string, binaryFor func(string) string) []namedQuotaReader {
	seen := make(map[string]bool, len(agents))
	readers := make([]namedQuotaReader, 0, len(agents))
	for _, agent := range agents {
		if seen[agent] {
			continue
		}
		seen[agent] = true
		// Which reader an agent uses is named by its definition, so an agent
		// that arrives from a config file asks for the reader it shares rather
		// than being matched on its own name. Registry-driven quota readers
		// beyond the two built in here are issue #489.
		reader := ""
		if definition, ok := agentDefinition(agent); ok {
			reader = definition.Quota.Reader
		}
		switch reader {
		case "codex":
			reader := &codexQuotaReader{Binary: binaryFor(agent)}
			readers = append(readers, namedQuotaReader{name: agent, read: reader.Read})
		case "claude":
			reader := &claudeQuotaReader{Binary: binaryFor(agent)}
			readers = append(readers, namedQuotaReader{name: agent, read: reader.Read})
		default:
			readers = append(readers, namedQuotaReader{name: agent, read: func(context.Context) string { return "" }})
		}
	}
	return readers
}

// combinedQuotaReader reads every named reader's quota and returns the
// results keyed by agent name.
func combinedQuotaReader(readers []namedQuotaReader) func(context.Context) map[string]string {
	return func(ctx context.Context) map[string]string {
		result := make(map[string]string, len(readers))
		for _, reader := range readers {
			result[reader.name] = reader.read(ctx)
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
