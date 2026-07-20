package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var errProjectWebhookUnavailable = errors.New("project push webhook unavailable")

type managedHook struct {
	ID     int `json:"id"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

type webhookSpec struct {
	apiPath string
	name    string
	events  []string
}

func (g GHCLI) ConfigureWebhook(ctx context.Context, value, endpoint, secret string) error {
	target, err := parseTarget(value)
	if err != nil {
		return err
	}
	spec, err := g.webhookSpec(ctx, target)
	if err != nil {
		return err
	}
	output, err := g.api(ctx, spec.apiPath, "")
	if err != nil {
		return webhookAccessError("list", spec, err)
	}
	var hooks []managedHook
	if err := json.Unmarshal(output, &hooks); err != nil {
		return fmt.Errorf("decode webhooks for %s: %w", spec.name, err)
	}
	found := false
	for _, hook := range hooks {
		if hook.Config.URL == endpoint {
			found = true
			continue
		}
		if ngrokURL(hook.Config.URL) {
			if _, err := g.api(ctx, fmt.Sprintf("%s/%d", spec.apiPath, hook.ID), "DELETE"); err != nil {
				return webhookAccessError(fmt.Sprintf("remove old ngrok webhook %d from", hook.ID), spec, err)
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
		"events": spec.events,
		"config": config,
	})
	if err != nil {
		return err
	}
	if _, err := g.api(ctx, spec.apiPath, "POST", string(body)); err != nil {
		return webhookAccessError("create", spec, err)
	}
	return nil
}

func (g GHCLI) webhookSpec(ctx context.Context, target target) (webhookSpec, error) {
	if !target.isProject {
		return webhookSpec{apiPath: "repos/" + target.repo + "/hooks", name: target.repo, events: []string{"issues", "push", "ping"}}, nil
	}
	ownerType := target.projectOwnerType
	if ownerType == "" {
		output, err := g.api(ctx, "users/"+target.owner, "")
		if err != nil {
			return webhookSpec{}, fmt.Errorf("identify project owner %s: %w", target.owner, err)
		}
		var owner struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(output, &owner); err != nil {
			return webhookSpec{}, fmt.Errorf("decode project owner %s: %w", target.owner, err)
		}
		if owner.Type == "Organization" {
			ownerType = "orgs"
		} else {
			ownerType = "users"
		}
	}
	if ownerType != "orgs" {
		return webhookSpec{}, fmt.Errorf("%w: GitHub only provides push events for organization-owned Projects; using periodic synchronization for the project owned by %s", errProjectWebhookUnavailable, target.owner)
	}
	return webhookSpec{
		apiPath: "orgs/" + target.owner + "/hooks",
		name:    "organization project " + target.owner,
		events:  []string{"projects_v2_item"},
	}, nil
}

func webhookAccessError(action string, spec webhookSpec, err error) error {
	wrapped := fmt.Errorf("%s webhooks for %s: %w", action, spec.name, err)
	if strings.HasPrefix(spec.apiPath, "orgs/") {
		return fmt.Errorf("%w; organization project push requires organization-owner access and the admin:org_hook scope (run `gh auth refresh -s admin:org_hook`)", wrapped)
	}
	return wrapped
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
