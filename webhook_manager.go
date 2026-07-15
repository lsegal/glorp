package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type repositoryHook struct {
	ID     int `json:"id"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

func (g GHCLI) ConfigureWebhook(ctx context.Context, repo, endpoint, secret string) error {
	target, err := parseTarget(repo)
	if err != nil || target.isProject || target.repo == "" {
		if err == nil {
			err = fmt.Errorf("webhooks require a repository target")
		}
		return err
	}
	output, err := g.api(ctx, "repos/"+target.repo+"/hooks", "")
	if err != nil {
		return fmt.Errorf("list webhooks for %s: %w", target.repo, err)
	}
	var hooks []repositoryHook
	if err := json.Unmarshal(output, &hooks); err != nil {
		return fmt.Errorf("decode webhooks for %s: %w", target.repo, err)
	}
	found := false
	for _, hook := range hooks {
		if hook.Config.URL == endpoint {
			found = true
			continue
		}
		if ngrokURL(hook.Config.URL) {
			if _, err := g.api(ctx, fmt.Sprintf("repos/%s/hooks/%d", target.repo, hook.ID), "DELETE"); err != nil {
				return fmt.Errorf("remove old ngrok webhook %d for %s: %w", hook.ID, target.repo, err)
			}
		}
	}
	if found {
		return nil
	}
	config := map[string]interface{}{
		"url":          endpoint,
		"content_type": "json",
		"insecure_ssl": "0",
	}
	if secret != "" {
		config["secret"] = secret
	}
	body, err := json.Marshal(map[string]interface{}{
		"name":   "web",
		"active": true,
		"events": []string{"issues", "push", "ping"},
		"config": config,
	})
	if err != nil {
		return err
	}
	if _, err := g.api(ctx, "repos/"+target.repo+"/hooks", "POST", string(body)); err != nil {
		return fmt.Errorf("create webhook for %s: %w", target.repo, err)
	}
	return nil
}

func (g GHCLI) api(ctx context.Context, path, method string, body ...string) ([]byte, error) {
	args := []string{"api", path}
	if method != "" {
		args = append(args, "--method", method)
	}
	cmd := exec.CommandContext(ctx, g.Binary, args...)
	if len(body) > 0 {
		cmd.Stdin = strings.NewReader(body[0])
		args = append(args, "--input", "-")
		cmd = exec.CommandContext(ctx, g.Binary, args...)
		cmd.Stdin = strings.NewReader(body[0])
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	return output, nil
}
