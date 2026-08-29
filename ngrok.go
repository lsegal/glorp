package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// NgrokTunnel owns the ngrok process and the public URL it assigned.
type NgrokTunnel struct {
	cmd       *exec.Cmd
	publicURL string
}

func (t *NgrokTunnel) URL() string { return t.publicURL }

func (t *NgrokTunnel) Close() error {
	if t == nil || t.cmd == nil {
		return nil
	}
	return stopChildProcess(t.cmd)
}

// ngrokLogRecord is the subset of ngrok's JSON log records glorp reads.
type ngrokLogRecord struct {
	Level   string `json:"lvl"`
	Message string `json:"msg"`
	URL     string `json:"url"`
	Err     string `json:"err"`
}

// ngrokLogWatcher reads the tunnel's public URL out of the log stream of the
// ngrok process glorp itself started. The local ngrok API cannot be used for
// this: it lives on a fixed port (4040) owned by whichever agent bound it
// first, so a second agent — an orphaned glorp tunnel, or an unrelated one —
// silently moves glorp's own API to 4041 and glorp adopts a stranger's tunnel
// URL, pointing the GitHub webhook at a dead port (issue #361).
//
// Only failures are forwarded to the wrapped writer, which keeps ngrok's
// routine chatter (including a benign Windows agent-path diagnostic) out of
// glorp's UI while still showing the user why a tunnel never came up.
type ngrokLogWatcher struct {
	mu      sync.Mutex
	out     io.Writer
	pending []byte
	url     string
	failure []string
}

func (w *ngrokLogWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		line := string(w.pending[:index])
		w.pending = w.pending[index+1:]
		w.consume(line)
	}
	return len(p), nil
}

// consume interprets one log line, recording the tunnel URL and any failure.
func (w *ngrokLogWatcher) consume(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	var record ngrokLogRecord
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		// Not a JSON record, so it cannot be classified; show it rather than
		// swallow output that may explain a failure.
		w.record(line, true)
		return
	}
	if record.Message == "started tunnel" && record.URL != "" && w.url == "" {
		w.url = strings.TrimRight(record.URL, "/")
	}
	if ngrokLogFailure(record.Level) {
		w.record(ngrokLogMessage(record), ngrokLogFatal(record.Level))
	}
}

// record keeps a failure for the error glorp returns if no tunnel comes up.
// Only fatal records are also printed, matching what the previous
// critical-only log level showed: ngrok reports recoverable problems (such as
// a host-info probe it could not run) at error level, and those would
// otherwise clutter glorp's UI on an otherwise healthy tunnel.
func (w *ngrokLogWatcher) record(message string, fatal bool) {
	w.failure = append(w.failure, message)
	if fatal && w.out != nil {
		fmt.Fprintln(w.out, message)
	}
}

func (w *ngrokLogWatcher) URL() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.url
}

// Failure summarizes the error output ngrok produced, for the error glorp
// returns when no tunnel is established.
func (w *ngrokLogWatcher) Failure() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.failure, "; ")
}

// ngrokLogFailure reports whether a log level means ngrok could not do its
// job. ngrok spells its error level both ways depending on log format.
func ngrokLogFailure(level string) bool {
	switch strings.ToLower(level) {
	case "eror", "error", "crit", "fatal":
		return true
	}
	return false
}

// ngrokLogFatal reports whether a log level means ngrok is giving up, as
// opposed to reporting a problem it recovered from.
func ngrokLogFatal(level string) bool {
	switch strings.ToLower(level) {
	case "crit", "fatal":
		return true
	}
	return false
}

func ngrokLogMessage(record ngrokLogRecord) string {
	message := "ngrok: " + record.Message
	if record.Err != "" && record.Err != "<nil>" {
		message += ": " + strings.TrimSpace(record.Err)
	}
	return message
}

func startNgrok(ctx context.Context, binary, listenAddr string, out io.Writer) (*NgrokTunnel, error) {
	watcher := &ngrokLogWatcher{out: out}
	cmd := exec.CommandContext(ctx, binary, ngrokArgs(listenAddr)...)
	cmd.Stdout, cmd.Stderr = watcher, watcher
	if err := startChildProcess(cmd); err != nil {
		return nil, fmt.Errorf("start ngrok: %w", err)
	}
	tunnel := &NgrokTunnel{cmd: cmd}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if tunnel.publicURL = watcher.URL(); tunnel.publicURL != "" {
			return tunnel, nil
		}
		select {
		case <-ctx.Done():
			_ = tunnel.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = tunnel.Close()
			return nil, ngrokStartError(watcher.Failure())
		case <-ticker.C:
		}
	}
}

func ngrokStartError(failure string) error {
	if failure == "" {
		return fmt.Errorf("wait for ngrok tunnel: timed out")
	}
	return fmt.Errorf("wait for ngrok tunnel: timed out (%s)", failure)
}

// ngrokArgs keeps ngrok from entering its interactive terminal dashboard,
// which clears glorp's UI, and asks for JSON records so glorp can read the
// tunnel URL of this exact process out of the stream. Info level is required
// because the "started tunnel" record carrying the URL is logged at it;
// ngrokLogWatcher filters the stream back down to failures.
func ngrokArgs(listenAddr string) []string {
	return []string{"http", "--log=stdout", "--log-format=json", "--log-level=info", listenAddr}
}

func webhookURL(publicURL, webhookPath string) (string, error) {
	u, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid ngrok public URL %q", publicURL)
	}
	if webhookPath == "" {
		webhookPath = "/webhook"
	}
	if !strings.HasPrefix(webhookPath, "/") {
		webhookPath = "/" + webhookPath
	}
	u.Path = strings.TrimRight(webhookPath, "/")
	return u.String(), nil
}

func ngrokURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && strings.Contains(strings.ToLower(u.Hostname()), "ngrok")
}
