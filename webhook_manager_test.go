package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestWebhookSpecSupportsOrganizationProjects(t *testing.T) {
	target, err := parseTarget("https://github.com/orgs/example/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := (GHCLI{}).webhookSpec(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if spec.apiPath != "orgs/example/hooks" || spec.name != "organization project example" || !reflect.DeepEqual(spec.events, []string{"projects_v2_item"}) {
		t.Fatalf("webhook spec = %#v", spec)
	}
}

func TestOrganizationWebhookErrorsReportRequiredAccess(t *testing.T) {
	spec := webhookSpec{apiPath: "orgs/example/hooks", name: "organization project example"}
	err := webhookAccessError("list", spec, errors.New("HTTP 404"))
	if !strings.Contains(err.Error(), "organization-owner") || !strings.Contains(err.Error(), "gh auth refresh -s admin:org_hook") {
		t.Fatalf("organization webhook error = %v", err)
	}
}

func TestWebhookSpecRejectsPersonalProjects(t *testing.T) {
	target, err := parseTarget("https://github.com/users/octocat/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (GHCLI{}).webhookSpec(context.Background(), target)
	if !errors.Is(err, errProjectWebhookUnavailable) || !strings.Contains(err.Error(), "periodic synchronization") {
		t.Fatalf("personal project webhook error = %v", err)
	}
}

func TestWebhookSpecPreservesRepositoryEvents(t *testing.T) {
	target, err := parseTarget("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := (GHCLI{}).webhookSpec(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if spec.apiPath != "repos/owner/repo/hooks" || !reflect.DeepEqual(spec.events, []string{"issues", "pull_request", "push", "ping"}) {
		t.Fatalf("webhook spec = %#v", spec)
	}
}
