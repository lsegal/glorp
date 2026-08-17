package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func projectItemsResponse(repos ...string) []byte {
	nodes := make([]string, 0, len(repos))
	for i, repo := range repos {
		nodes = append(nodes, fmt.Sprintf(`{"id":"item%d","content":{"__typename":"Issue","number":%d,"title":"t","state":"OPEN","repository":{"nameWithOwner":%q}}}`, i, i+1, repo))
	}
	return []byte(fmt.Sprintf(`{"data":{"user":{"projectV2":{"items":{"nodes":[%s],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`, strings.Join(nodes, ",")))
}

func TestWebhookSpecsSupportOrganizationProjects(t *testing.T) {
	target, err := parseTarget("https://github.com/orgs/example/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	specs, err := (GHCLI{}).webhookSpecs(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("webhook specs = %#v", specs)
	}
	if specs[0].apiPath != "orgs/example/hooks" || specs[0].name != "organization project example" || !reflect.DeepEqual(specs[0].events, []string{"projects_v2_item"}) {
		t.Fatalf("webhook spec = %#v", specs[0])
	}
}

func TestOrganizationWebhookErrorsReportRequiredAccess(t *testing.T) {
	spec := webhookSpec{apiPath: "orgs/example/hooks", name: "organization project example"}
	err := webhookAccessError("list", spec, errors.New("HTTP 404"))
	if !strings.Contains(err.Error(), "organization-owner") || !strings.Contains(err.Error(), "gh auth refresh -s admin:org_hook") {
		t.Fatalf("organization webhook error = %v", err)
	}
}

// A user-owned project gets no projects_v2 webhook from GitHub, so push mode
// has to watch the repositories backing the board instead of falling back to
// polling for the whole target (issue #234).
func TestWebhookSpecsWatchPersonalProjectRepositories(t *testing.T) {
	target, err := parseTarget("https://github.com/users/octocat/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "api" || args[1] != "graphql" {
			return nil, fmt.Errorf("unexpected command %v", args)
		}
		return projectItemsResponse("octocat/beta", "octocat/alpha", "octocat/beta"), nil
	}}
	specs, err := gh.webhookSpecs(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("webhook specs = %#v", specs)
	}
	if specs[0].apiPath != "repos/octocat/alpha/hooks" || specs[1].apiPath != "repos/octocat/beta/hooks" {
		t.Fatalf("webhook specs = %#v", specs)
	}
	if !reflect.DeepEqual(specs[0].events, []string{"issues", "pull_request", "push", "ping", "issue_comment"}) {
		t.Fatalf("webhook spec events = %#v", specs[0].events)
	}
}

// The board query ignores the ready filter: a repository has to be watched
// before any of its issues become ready.
func TestPersonalProjectRepositoryLookupIgnoresFilter(t *testing.T) {
	target, err := parseTarget("https://github.com/users/octocat/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	var itemQuery string
	gh := GHCLI{Filter: "label:agent-ready", runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		for i, arg := range args {
			if strings.HasPrefix(arg, "itemQuery=") {
				itemQuery = strings.TrimPrefix(args[i], "itemQuery=")
			}
		}
		return projectItemsResponse("octocat/alpha"), nil
	}}
	if _, err := gh.webhookSpecs(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(itemQuery, "agent-ready") {
		t.Fatalf("project repository lookup applied the ready filter: %q", itemQuery)
	}
}

func TestWebhookSpecsReportEmptyPersonalProject(t *testing.T) {
	target, err := parseTarget("https://github.com/users/octocat/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		return projectItemsResponse(), nil
	}}
	_, err = gh.webhookSpecs(context.Background(), target)
	if !errors.Is(err, errProjectWebhookUnavailable) || !strings.Contains(err.Error(), "periodic synchronization") {
		t.Fatalf("empty personal project webhook error = %v", err)
	}
}

func TestWebhookSpecsPreserveRepositoryEvents(t *testing.T) {
	target, err := parseTarget("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	specs, err := (GHCLI{}).webhookSpecs(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].apiPath != "repos/owner/repo/hooks" || !reflect.DeepEqual(specs[0].events, []string{"issues", "pull_request", "push", "ping", "issue_comment"}) {
		t.Fatalf("webhook specs = %#v", specs)
	}
}
