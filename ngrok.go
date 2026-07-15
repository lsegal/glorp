package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// NgrokTunnel owns the ngrok process and the public URL it assigned.
type NgrokTunnel struct {
	cmd       *exec.Cmd
	publicURL string
}

func (t *NgrokTunnel) URL() string { return t.publicURL }

func (t *NgrokTunnel) Close() error {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	if err := t.cmd.Process.Kill(); err != nil {
		return err
	}
	return t.cmd.Wait()
}

type ngrokTunnels struct {
	Tunnels []struct {
		PublicURL string `json:"public_url"`
		Proto     string `json:"proto"`
	} `json:"tunnels"`
}

func decodeNgrokURL(data []byte) (string, error) {
	var response ngrokTunnels
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("decode ngrok tunnels: %w", err)
	}
	for _, tunnel := range response.Tunnels {
		if tunnel.Proto == "https" && tunnel.PublicURL != "" {
			return strings.TrimRight(tunnel.PublicURL, "/"), nil
		}
	}
	for _, tunnel := range response.Tunnels {
		if tunnel.PublicURL != "" {
			return strings.TrimRight(tunnel.PublicURL, "/"), nil
		}
	}
	return "", fmt.Errorf("ngrok did not report a public tunnel")
}

func startNgrok(ctx context.Context, binary, listenAddr, apiURL string, out io.Writer) (*NgrokTunnel, error) {
	if apiURL == "" {
		apiURL = "http://127.0.0.1:4040"
	}
	cmd := exec.CommandContext(ctx, binary, "http", listenAddr)
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ngrok: %w", err)
	}
	tunnel := &NgrokTunnel{cmd: cmd}
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/api/tunnels", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode == http.StatusOK && readErr == nil {
					if tunnel.publicURL, err = decodeNgrokURL(body); err == nil {
						return tunnel, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			_ = tunnel.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = tunnel.Close()
			return nil, fmt.Errorf("wait for ngrok tunnel: timed out")
		case <-ticker.C:
		}
	}
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
