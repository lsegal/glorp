package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

var errProjectWebhookUnavailable = errors.New("project push webhook unavailable")

// errWebhookPartiallyConfigured reports that at least one, but not every,
// webhook a target needs could be configured. Push mode still works for the
// repositories that succeeded, so this is informational rather than fatal.
var errWebhookPartiallyConfigured = errors.New("some push webhooks could not be configured")

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
	specs, err := g.webhookSpecs(ctx, target)
	if err != nil {
		return err
	}
	failures := make([]error, 0, len(specs))
	for _, spec := range specs {
		if err := g.configureWebhookSpec(ctx, spec, endpoint, secret); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	if len(failures) == len(specs) {
		return errors.Join(failures...)
	}
	return fmt.Errorf("%w (%d of %d): %w", errWebhookPartiallyConfigured, len(failures), len(specs), errors.Join(failures...))
}

func (g GHCLI) configureWebhookSpec(ctx context.Context, spec webhookSpec, endpoint, secret string) error {
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

// webhookSpecs lists every webhook a target needs. A repository target and an
// organization-owned project each need exactly one. GitHub publishes no
// projects_v2 webhook for user-owned projects, so those fall back to a
// repository webhook on each repository currently backing the board's items;
// that pushes new issues immediately instead of leaving the whole target to
// the periodic poller (issue #234). Board-only changes, such as dragging an
// existing issue onto the board or moving a card between columns, still
// surface through periodic synchronization.
func (g GHCLI) webhookSpecs(ctx context.Context, target target) ([]webhookSpec, error) {
	if !target.isProject {
		return []webhookSpec{repositoryWebhookSpec(target.repo)}, nil
	}
	ownerType, err := g.projectOwnerType(ctx, target)
	if err != nil {
		return nil, err
	}
	if ownerType == "orgs" {
		return []webhookSpec{{
			apiPath: "orgs/" + target.owner + "/hooks",
			name:    "organization project " + target.owner,
			events:  []string{"projects_v2_item"},
		}}, nil
	}
	target.projectOwnerType = ownerType
	repos, err := g.projectRepositories(ctx, target)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("%w: GitHub only provides push events for organization-owned Projects and the project owned by %s has no issues to watch; using periodic synchronization", errProjectWebhookUnavailable, target.owner)
	}
	specs := make([]webhookSpec, 0, len(repos))
	for _, repo := range repos {
		specs = append(specs, repositoryWebhookSpec(repo))
	}
	return specs, nil
}

func repositoryWebhookSpec(repo string) webhookSpec {
	return webhookSpec{
		apiPath: "repos/" + repo + "/hooks",
		name:    repo,
		events:  []string{"issues", "pull_request", "push", "ping", "issue_comment"},
	}
}

func (g GHCLI) projectOwnerType(ctx context.Context, target target) (string, error) {
	if target.projectOwnerType != "" {
		return target.projectOwnerType, nil
	}
	output, err := g.api(ctx, "users/"+target.owner, "")
	if err != nil {
		return "", fmt.Errorf("identify project owner %s: %w", target.owner, err)
	}
	var owner struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(output, &owner); err != nil {
		return "", fmt.Errorf("decode project owner %s: %w", target.owner, err)
	}
	if owner.Type == "Organization" {
		return "orgs", nil
	}
	return "users", nil
}

// projectRepositories lists the distinct repositories backing a project's
// items. Filters are deliberately ignored: a webhook has to be in place before
// an issue becomes ready, so every repository on the board is watched.
func (g GHCLI) projectRepositories(ctx context.Context, target target) ([]string, error) {
	items, err := g.listProjectItems(ctx, target, "", true)
	if err != nil {
		return nil, fmt.Errorf("list repositories for project owned by %s: %w", target.owner, err)
	}
	seen := make(map[string]bool, len(items))
	repos := make([]string, 0, len(items))
	for _, item := range items {
		if item.Content == nil {
			continue
		}
		repo := strings.TrimSpace(item.Content.Repository)
		if repo == "" || seen[repo] {
			continue
		}
		seen[repo] = true
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return repos, nil
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
