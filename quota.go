package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
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
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
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

// namedQuotaReader reads quota text for a single named agent.
type namedQuotaReader struct {
	name string
	read func(context.Context) string
}

// namedQuotaReaders builds one quota reader per configured agent, deduplicated
// by name. Agents without a known quota source (e.g. claude) still get an
// entry so the UI can show that their quota is not tracked.
func namedQuotaReaders(agents []string, binaryFor func(string) string) []namedQuotaReader {
	seen := make(map[string]bool, len(agents))
	readers := make([]namedQuotaReader, 0, len(agents))
	for _, agent := range agents {
		if seen[agent] {
			continue
		}
		seen[agent] = true
		switch agent {
		case "codex":
			reader := &codexQuotaReader{Binary: binaryFor(agent)}
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
