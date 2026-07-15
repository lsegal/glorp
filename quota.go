package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	for _, request := range []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"gh-watch","title":"gh-watch","version":"dev"}}}`,
		`{"method":"initialized","params":{}}`,
		`{"id":2,"method":"account/rateLimits/read","params":null}`,
	} {
		if _, err := fmt.Fprintln(stdin, request); err != nil {
			return "", err
		}
	}
	if err := stdin.Close(); err != nil {
		return "", err
	}
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

func quotaText(snapshot WatchSnapshot) string {
	if strings.TrimSpace(snapshot.Quota) != "" {
		return "quota: " + snapshot.Quota
	}
	if snapshot.TokenLimit > 0 {
		return fmt.Sprintf("tokens: %d/%d", snapshot.TokensUsed, snapshot.TokenLimit)
	}
	return "quota: unavailable"
}
