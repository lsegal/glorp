package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mattn/go-isatty"
)

func TestListenForWebhooksAssignsRandomPort(t *testing.T) {
	listener, err := listenForWebhooks("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if port == "0" || port == "" {
		t.Fatalf("listener address = %q, want an assigned port", listener.Addr())
	}
}

func TestOriginatingWorkStateLoadsLinkedPullRequest(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"state":"OPEN"}`),
		[]byte(`[{"event":"cross-referenced","source":{"issue":{"number":9,"body":"Closes #7","pull_request":{"merged_at":null}}}}]`),
		[]byte(`{"state":"closed","merged_at":null}`),
	}
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return responses[len(calls)-1], nil
	}}
	state, err := gh.OriginatingWorkState(context.Background(), "owner/repo", 7)
	if err != nil || state.IssueState != "OPEN" || len(state.PullRequests) != 1 || state.PullRequests[0] != (PullRequestWorkState{Number: 9, State: "closed"}) {
		t.Fatalf("OriginatingWorkState() = (%#v, %v)", state, err)
	}
	want := []string{"api", "repos/owner/repo/pulls/9"}
	if !reflect.DeepEqual(calls[2], want) {
		t.Fatalf("pull request state call = %#v, want %#v", calls[2], want)
	}
}

func TestClosedWorkReasonDistinguishesManualIssueClosureFromMerge(t *testing.T) {
	for _, test := range []struct {
		name       string
		issue      string
		timeline   string
		pull       string
		wantReason string
	}{
		{name: "manual closure", issue: `{"state":"CLOSED"}`, timeline: `[]`, wantReason: "issue #7 was closed without a merge"},
		{name: "merged pull request", issue: `{"state":"CLOSED"}`, timeline: `[{"event":"cross-referenced","source":{"issue":{"number":9,"body":"Closes #7","pull_request":{}}}}]`, pull: `{"state":"closed","merged_at":"2026-07-20T12:00:00Z"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			responses := [][]byte{[]byte(test.issue)}
			responses = append(responses, []byte(test.timeline))
			if test.pull != "" {
				responses = append(responses, []byte(test.pull))
			}
			call := 0
			gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
				response := responses[call]
				call++
				return response, nil
			}}
			state, err := gh.OriginatingWorkState(context.Background(), "owner/repo", 7)
			reason := closedWorkReason(OriginatingWorkState{IssueState: "OPEN"}, state, 7)
			if err != nil || reason != test.wantReason {
				t.Fatalf("closure state = (%#v, %v), reason=%q, want %q", state, err, reason, test.wantReason)
			}
		})
	}
}

func TestGHCLIPostComment(t *testing.T) {
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{}`), nil
	}}
	if err := gh.PostComment(context.Background(), "owner/repo", 7, "Starting work on this issue /glorp:AAA"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	want := []string{"api", "repos/owner/repo/issues/7/comments", "-f", "body=Starting work on this issue /glorp:AAA"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("call = %#v, want %#v", calls[0], want)
	}
}

func TestGHCLIPostCommentReturnsError(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit status 1")
	}}
	if err := gh.PostComment(context.Background(), "owner/repo", 7, "hi"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGHCLIListComments(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`[{"body":"Starting work on this issue /glorp:AAA","created_at":"2026-07-20T12:00:00Z"}]`), nil
	}}
	comments, err := gh.ListComments(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "Starting work on this issue /glorp:AAA" {
		t.Fatalf("comments = %#v", comments)
	}
}

func TestGHCLIListCommentsReturnsError(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit status 1")
	}}
	if _, err := gh.ListComments(context.Background(), "owner/repo", 7); err == nil {
		t.Fatal("expected an error")
	}
}

func TestValidRepo(t *testing.T) {
	for _, s := range []string{"owner/repo", "a/b"} {
		if !validRepo(s) {
			t.Errorf("%q", s)
		}
	}
	for _, s := range []string{"repo", "a/b/c", "a b/c", "/repo"} {
		if validRepo(s) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestVersionDefaultsToDevelopment(t *testing.T) {
	if version != "dev" {
		t.Fatalf("version = %q, want dev", version)
	}
}

func TestSelectedUIMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  string
		noUI  bool
		want  string
		valid bool
	}{
		{name: "web", mode: "web", want: "web", valid: true},
		{name: "tui", mode: "tui", want: "tui", valid: true},
		{name: "none", mode: "none", want: "none", valid: true},
		{name: "no ui alias", mode: "web", noUI: true, want: "none", valid: true},
		{name: "invalid", mode: "desktop"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectedUIMode(test.mode, test.noUI)
			if (err == nil) != test.valid || got != test.want {
				t.Fatalf("selectedUIMode(%q, %t) = (%q, %v), want (%q, valid=%t)", test.mode, test.noUI, got, err, test.want, test.valid)
			}
		})
	}
}

func TestShouldUseTerminalUI(t *testing.T) {
	if shouldUseTerminalUI("web", os.Stdout) || shouldUseTerminalUI("none", os.Stdout) {
		t.Fatal("non-terminal UI modes selected the terminal UI")
	}
	if got, want := shouldUseTerminalUI("tui", os.Stdout), isTerminal(os.Stdout); got != want {
		t.Fatalf("shouldUseTerminalUI(tui) = %t, want %t", got, want)
	}
}

func TestParseTargetURLs(t *testing.T) {
	for _, input := range []string{
		"https://github.com/lsegal/glorp",
		"https://github.com/lsegal/glorp/",
	} {
		got, err := parseTarget(input)
		if err != nil || got.repo != "lsegal/glorp" || got.isProject {
			t.Fatalf("parseTarget(%q) = %#v, %v", input, got, err)
		}
	}
	got, err := parseTarget("https://github.com/users/lsegal/projects/3")
	if err != nil || !got.isProject || got.owner != "lsegal" || got.projectID != "3" || got.projectOwnerType != "users" {
		t.Fatalf("project target = %#v, %v", got, err)
	}
	got, err = parseTarget("https://github.com/orgs/example/projects/4")
	if err != nil || !got.isProject || got.owner != "example" || got.projectID != "4" || got.projectOwnerType != "orgs" {
		t.Fatalf("organization project target = %#v, %v", got, err)
	}
	for _, input := range []string{
		"https://github.com/lsegal/glorp/discussions",
		"https://github.com/lsegal/glorp/discussions/",
	} {
		got, err := parseTarget(input)
		if err != nil || !got.isDiscussion || got.repo != "lsegal/glorp" || got.isProject || got.discussionCategory != "" {
			t.Fatalf("parseTarget(%q) = %#v, %v", input, got, err)
		}
	}
	got, err = parseTarget("https://github.com/lsegal/glorp/discussions/categories/q-a")
	if err != nil || !got.isDiscussion || got.repo != "lsegal/glorp" || got.discussionCategory != "q-a" {
		t.Fatalf("discussion category target = %#v, %v", got, err)
	}
	if _, err := parseTarget("https://github.com/lsegal/glorp/discussions/categories/"); err == nil {
		t.Fatal("expected an error for an empty discussion category")
	}
}

func TestExpandTargetShorthand(t *testing.T) {
	origin := func() (string, bool) { return "lsegal/glorp", true }
	for _, tt := range []struct{ in, want string }{
		{"projects:3", "https://github.com/lsegal/glorp/projects/3"},
		{"projects:other/repo/4", "https://github.com/other/repo/projects/4"},
		{"discussions:q-a", "https://github.com/lsegal/glorp/discussions/categories/q-a"},
		{"discussions:other/repo/q-a", "https://github.com/other/repo/discussions/categories/q-a"},
		{"discussions:other/repo", "https://github.com/other/repo/discussions"},
		{"discussions:/other/repo/q-a/", "https://github.com/other/repo/discussions/categories/q-a"},
		{"owner/repo", "owner/repo"},
		{"https://github.com/users/lsegal/projects/3", "https://github.com/users/lsegal/projects/3"},
	} {
		got, err := expandTargetShorthand(tt.in, origin)
		if err != nil || got != tt.want {
			t.Fatalf("expandTargetShorthand(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		if _, err := parseTarget(got); err != nil {
			t.Fatalf("parseTarget(%q) = %v", got, err)
		}
	}
	for _, in := range []string{"projects:", "projects:lsegal/3", "projects:abc", "projects:other/repo/abc", "discussions:a/b/c/d"} {
		if got, err := expandTargetShorthand(in, origin); err == nil {
			t.Fatalf("expandTargetShorthand(%q) = %q, want an error", in, got)
		}
	}
}

func TestExpandTargetShorthandWithoutOriginRemote(t *testing.T) {
	none := func() (string, bool) { return "", false }
	for _, in := range []string{"projects:3", "discussions:q-a"} {
		if _, err := expandTargetShorthand(in, none); err == nil {
			t.Fatalf("expandTargetShorthand(%q) without an origin remote should fail", in)
		}
	}
	if got, err := expandTargetShorthand("owner/repo", none); err != nil || got != "owner/repo" {
		t.Fatalf("expandTargetShorthand(owner/repo) = %q, %v", got, err)
	}
}

func TestGHCLIListUnansweredDiscussions(t *testing.T) {
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{"data":{"repository":{"discussions":{"nodes":[
			{"number":5,"title":"Unanswered","body":"question","createdAt":"2026-08-01T00:00:00Z","comments":{"totalCount":0}},
			{"number":3,"title":"Answered","body":"question","createdAt":"2026-07-01T00:00:00Z","comments":{"totalCount":2}}
		]}}}}`), nil
	}}
	discussions, err := gh.ListUnansweredDiscussions(context.Background(), "https://github.com/owner/repo/discussions")
	if err != nil {
		t.Fatalf("ListUnansweredDiscussions: %v", err)
	}
	if len(discussions) != 1 || discussions[0].Number != 5 || discussions[0].Title != "Unanswered" {
		t.Fatalf("discussions = %#v", discussions)
	}
	if len(calls) != 1 || calls[0][0] != "api" || calls[0][1] != "graphql" {
		t.Fatalf("call = %#v", calls)
	}
}

func TestGHCLIListUnansweredDiscussionsFiltersByCategory(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"data":{"repository":{"discussions":{"nodes":[
			{"number":5,"title":"Question","body":"q","createdAt":"2026-08-01T00:00:00Z","comments":{"totalCount":0},"category":{"slug":"q-a","name":"Q&A"}},
			{"number":6,"title":"Idea","body":"i","createdAt":"2026-08-02T00:00:00Z","comments":{"totalCount":0},"category":{"slug":"ideas","name":"Ideas"}}
		]}}}}`), nil
	}}
	for _, target := range []string{
		"https://github.com/owner/repo/discussions/categories/q-a",
		"https://github.com/owner/repo/discussions/categories/Q-A",
		"https://github.com/owner/repo/discussions/categories/Q&A",
	} {
		discussions, err := gh.ListUnansweredDiscussions(context.Background(), target)
		if err != nil {
			t.Fatalf("ListUnansweredDiscussions(%q): %v", target, err)
		}
		if len(discussions) != 1 || discussions[0].Number != 5 {
			t.Fatalf("discussions for %q = %#v", target, discussions)
		}
	}
	discussions, err := gh.ListUnansweredDiscussions(context.Background(), "https://github.com/owner/repo/discussions")
	if err != nil || len(discussions) != 2 {
		t.Fatalf("uncategorized target = %#v, %v", discussions, err)
	}
}

func TestGHCLIListUnansweredDiscussionsRejectsNonDiscussionTarget(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		t.Fatal("runCommand should not be called for a non-discussion target")
		return nil, nil
	}}
	if _, err := gh.ListUnansweredDiscussions(context.Background(), "owner/repo"); err == nil {
		t.Fatal("expected an error for a non-discussion target")
	}
}

func TestGHCLIListUnansweredDiscussionsReturnsError(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit status 1")
	}}
	if _, err := gh.ListUnansweredDiscussions(context.Background(), "https://github.com/owner/repo/discussions"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseGitHubRemote(t *testing.T) {
	for _, input := range []string{
		"https://github.com/lsegal/glorp",
		"https://github.com/lsegal/glorp.git",
		"git@github.com:lsegal/glorp.git",
		"ssh://git@github.com/lsegal/glorp.git",
	} {
		repo, ok := parseGitHubRemote(input)
		if !ok || repo != "lsegal/glorp" {
			t.Fatalf("parseGitHubRemote(%q) = %q, %v", input, repo, ok)
		}
	}
	for _, input := range []string{
		"",
		"https://gitlab.com/lsegal/glorp",
		"git@gitlab.com:lsegal/glorp.git",
	} {
		if _, ok := parseGitHubRemote(input); ok {
			t.Fatalf("parseGitHubRemote(%q) unexpectedly succeeded", input)
		}
	}
}

func TestOriginRemoteTarget(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://github.com/lsegal/glorp.git")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	repo, ok := originRemoteTarget()
	if !ok || repo != "lsegal/glorp" {
		t.Fatalf("originRemoteTarget() = %q, %v", repo, ok)
	}
}

func TestIssueRepositoryUsesProjectItemRepository(t *testing.T) {
	issue := Issue{Number: 32, Repository: "lsegal/glorp"}
	if got := issueRepository("https://github.com/users/lsegal/projects/3", issue); got != "lsegal/glorp" {
		t.Fatalf("issue repository = %q", got)
	}
}

func TestIssueRepositoryNormalizesRepositoryURL(t *testing.T) {
	issue := Issue{Number: 32}
	if got := issueRepository("https://github.com/lsegal/glorp", issue); got != "lsegal/glorp" {
		t.Fatalf("issue repository = %q", got)
	}
}

func TestProjectListArgs(t *testing.T) {
	got := projectListArgs(target{owner: "lsegal", projectID: "3", isProject: true}, "label:other status=closed", false)
	want := []string{"project", "item-list", "3", "--owner", "lsegal", "--format", "json", "--limit", "1000", "--query", "is:issue is:open label:other status=closed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestProjectListArgsOmitsDefaultFilter(t *testing.T) {
	got := projectListArgs(target{owner: "lsegal", projectID: "3", isProject: true}, defaultIssueFilter, false)
	want := []string{"project", "item-list", "3", "--owner", "lsegal", "--format", "json", "--limit", "1000", "--query", "is:issue is:open"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestUserProjectListUsesURLOwnerType(t *testing.T) {
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{"data":{"user":{"projectV2":{"items":{"nodes":[{"id":"PVTI_item","fieldValueByName":{"name":"Todo"},"content":{"__typename":"Issue","number":171,"title":"bug","body":"details","state":"OPEN","createdAt":"2026-07-20T17:38:43Z","repository":{"nameWithOwner":"lsegal/glorp"},"labels":{"nodes":[{"name":"agent-started"}]}}}],"pageInfo":{"hasNextPage":false,"endCursor":"cursor"}}}}}}`), nil
	}}
	issues, err := gh.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) < 4 || calls[0][0] != "api" || calls[0][1] != "graphql" || !strings.Contains(calls[0][3], "user(login:$login)") {
		t.Fatalf("gh calls = %#v, want typed user GraphQL query", calls)
	}
	if len(issues) != 1 || issues[0].Number != 171 || issues[0].Repository != "lsegal/glorp" || issues[0].ProjectStatus != "Todo" || issues[0].ProjectItemID != "PVTI_item" || len(issues[0].Labels) != 1 {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestOrganizationProjectListUsesURLOwnerTypeAndPaginates(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"data":{"organization":{"projectV2":{"items":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}}`),
		[]byte(`{"data":{"organization":{"projectV2":{"items":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":"done"}}}}}}`),
	}
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return responses[len(calls)-1], nil
	}}
	if _, err := gh.ListIssues(context.Background(), "https://github.com/orgs/example/projects/4"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0][3], "organization(login:$login)") || !slices.Contains(calls[1], "after=next") {
		t.Fatalf("gh calls = %#v, want paginated organization GraphQL queries", calls)
	}
}

func TestDecodeProjectIssues(t *testing.T) {
	got, err := decodeProjectIssues([]byte(`{"items":[{"id":"PVTI_item","status":"In Progress","content":{"number":7,"title":"bug","repository":"owner/repo","type":"Issue"}},{"content":{"number":8,"type":"PullRequest"}}]}`), nil)
	if err != nil || len(got) != 1 || got[0].Number != 7 || got[0].Repository != "owner/repo" || got[0].ProjectStatus != "In Progress" || got[0].ProjectItemID != "PVTI_item" {
		t.Fatalf("decode project issues = %#v, %v", got, err)
	}
}

func TestDecodeProjectItemsArray(t *testing.T) {
	items, err := decodeProjectItems([]byte(`[{"id":"PVTI_item","content":{"number":7,"type":"Issue"}}]`), nil)
	if err != nil || len(items) != 1 || items[0].ID != "PVTI_item" || items[0].Content.Number != 7 {
		t.Fatalf("decode project items = %#v, %v", items, err)
	}
}

func TestDecodeProjectFields(t *testing.T) {
	fields, err := decodeProjectFields([]byte(`{"fields":[{"id":"PVTF_status","name":"Status","options":[{"id":"opt_progress","name":"In Progress"}]}]}`), nil)
	if err != nil || len(fields) != 1 || fields[0].ID != "PVTF_status" || fields[0].Options[0].ID != "opt_progress" {
		t.Fatalf("decode project fields = %#v, %v", fields, err)
	}
}

func TestDecodeProjectFieldsIncludesOutputDetailOnFailure(t *testing.T) {
	_, err := decodeProjectFields([]byte("missing required scopes [project]"), errors.New("exit status 1"))
	if err == nil || !strings.Contains(err.Error(), "missing required scopes [project]") {
		t.Fatalf("decodeProjectFields() error = %v, want it to include the gh output detail", err)
	}
}

func TestDecodeRepositoryProjectItemsPage(t *testing.T) {
	var page repositoryProjectItemsPage
	err := json.Unmarshal([]byte(`{"data":{"repository":{"issue":{"projectItems":{"nodes":[{"id":"PVTI_item","project":{"id":"PVT_project","number":3,"owner":{"login":"owner"}}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor"}}}}}}`), &page)
	items := page.Data.Repository.Issue.ProjectItems
	if err != nil || len(items.Nodes) != 1 || items.Nodes[0].ID != "PVTI_item" || items.Nodes[0].Project.ID != "PVT_project" || items.Nodes[0].Project.Number != 3 || items.Nodes[0].Project.Owner.Login != "owner" {
		t.Fatalf("repository project items = %#v, %v", items.Nodes, err)
	}
	if !items.PageInfo.HasNextPage || items.PageInfo.EndCursor != "cursor" {
		t.Fatalf("repository project page info = %#v", items.PageInfo)
	}
}

func TestSetIssueStatusProjectItemLookupSurfacesFailureDetail(t *testing.T) {
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("some other project error"), errors.New("exit status 1")
	}}
	err := gh.SetIssueStatus(context.Background(), "https://github.com/users/owner/projects/3", Issue{Number: 7}, "In Progress")
	if err == nil || !strings.Contains(err.Error(), "some other project error") {
		t.Fatalf("SetIssueStatus() error = %v, want it to include the gh output detail", err)
	}
	want := projectListArgs(target{owner: "owner", projectID: "3", isProject: true}, "", true)
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("gh calls = %#v, want single call %#v", calls, want)
	}
}

func TestRepositoryIssueStatusUpdatesAttachedProject(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"data":{"repository":{"issue":{"projectItems":{"nodes":[{"id":"PVTI_item","project":{"id":"PVT_project","number":3,"owner":{"login":"owner"}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`),
		[]byte(`{"id":"PVT_project"}`),
		[]byte(`{"fields":[{"id":"PVTSSF_status","name":"Status","options":[{"id":"opt_progress","name":"In Progress"}]}]}`),
		nil,
	}
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) > len(responses) {
			t.Fatalf("unexpected gh call: %#v", args)
		}
		return responses[len(calls)-1], nil
	}}
	if err := gh.SetIssueStatus(context.Background(), "owner/repo", Issue{Number: 148}, "In Progress"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("gh calls = %#v, want GraphQL lookup plus three project update calls", calls)
	}
	if got := calls[0][:2]; !reflect.DeepEqual(got, []string{"api", "graphql"}) {
		t.Fatalf("project lookup call = %#v", calls[0])
	}
	wantEdit := []string{"project", "item-edit", "--id", "PVTI_item", "--field-id", "PVTSSF_status", "--project-id", "PVT_project", "--single-select-option-id", "opt_progress"}
	if !reflect.DeepEqual(calls[3], wantEdit) {
		t.Fatalf("project edit call = %#v, want %#v", calls[3], wantEdit)
	}
}

func TestRepositoryIssueStatusReportsProjectViewFailureDetail(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"data":{"repository":{"issue":{"projectItems":{"nodes":[{"id":"PVTI_item","project":{"id":"PVT_project","number":3,"owner":{"login":"owner"}}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`),
		[]byte("missing required scopes [read:project]"),
	}
	call := 0
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		response := responses[call]
		call++
		if call == 2 {
			return response, errors.New("exit status 1")
		}
		return response, nil
	}}
	err := gh.SetIssueStatus(context.Background(), "owner/repo", Issue{Number: 148}, "In Progress")
	if err == nil || !strings.Contains(err.Error(), "missing required scopes [read:project]") {
		t.Fatalf("SetIssueStatus() error = %v, want it to include the gh output detail", err)
	}
}

func TestProjectStatusOptionMatchesReadyStates(t *testing.T) {
	fields := []projectField{{
		ID:   "status-field",
		Name: "Status",
		Options: []projectFieldOption{
			{ID: "backlog-option", Name: "Backlog"},
			{ID: "ready-option", Name: "READY"},
			{ID: "custom-option", Name: "Agent Queue"},
		},
	}}
	for _, test := range []struct {
		status        string
		allowFallback bool
		wantOption    string
	}{
		{status: "ready", wantOption: "ready-option"},
		{status: "Todo", allowFallback: true, wantOption: "ready-option"},
		{status: "agent queue", wantOption: "custom-option"},
		{status: "Todo", wantOption: ""},
	} {
		fieldID, optionID := projectStatusOption(fields, test.status, test.allowFallback)
		if fieldID != "status-field" || optionID != test.wantOption {
			t.Errorf("projectStatusOption(%q, %v) = (%q, %q), want (%q, %q)", test.status, test.allowFallback, fieldID, optionID, "status-field", test.wantOption)
		}
	}
}

func TestDecodeProjectIssuesReportsMissingScope(t *testing.T) {
	_, err := decodeProjectIssues([]byte("error: your authentication token is missing required scopes [read:project]"), errors.New("exit status 1"))
	if err == nil || !strings.Contains(err.Error(), "gh auth refresh -s read:project") {
		t.Fatalf("missing scope error = %v", err)
	}
}

func TestProjectStatusErrorReportsWriteScope(t *testing.T) {
	detail := "error: your authentication token is missing required scopes [project]"
	if !strings.Contains(projectStatusError(45, errors.New("exit status 1"), detail).Error(), "gh auth refresh -s project") {
		t.Fatal("project status error did not report the project scope")
	}
}

func TestFilterFlagAccumulatesValues(t *testing.T) {
	got := filterFlag{values: []string{defaultIssueFilter}}
	if err := got.Set("label:bug"); err != nil {
		t.Fatal(err)
	}
	if err := got.Set("author:lsegal"); err != nil {
		t.Fatal(err)
	}
	if got.String() != "label:bug author:lsegal" {
		t.Fatalf("filter = %q", got.String())
	}
}

func TestFilterFlagDefaultsToMyOpenIssues(t *testing.T) {
	got := filterFlag{values: []string{defaultIssueFilter}}
	if got.String() != defaultIssueFilter {
		t.Fatalf("filter = %q, want %q", got.String(), defaultIssueFilter)
	}
}

func TestSplitAllowedCommenters(t *testing.T) {
	if got := splitAllowedCommenters(""); got != nil {
		t.Fatalf("splitAllowedCommenters(%q) = %#v, want nil", "", got)
	}
	got := splitAllowedCommenters(" lsegal, other , ,third")
	want := []string{"lsegal", "other", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitAllowedCommenters = %#v, want %#v", got, want)
	}
}

func TestAgentFlagAccumulatesValues(t *testing.T) {
	got := agentFlag{values: []agentSpec{{Name: "codex"}}}
	if err := got.Set("claude"); err != nil {
		t.Fatal(err)
	}
	if err := got.Set("codex"); err != nil {
		t.Fatal(err)
	}
	if want := "claude,codex"; got.String() != want {
		t.Fatalf("agents = %q, want %q", got.String(), want)
	}
}

func TestAgentFlagRejectsUnknownAgent(t *testing.T) {
	got := agentFlag{values: []agentSpec{{Name: "codex"}}}
	if err := got.Set("gemini"); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

func TestBugReportURL(t *testing.T) {
	got, err := bugReportURL("owner/repo", Issue{Number: 12}, []string{"agent", "--flag"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil || u.Path != "/owner/repo/issues/new" {
		t.Fatalf("URL = %q, %v", got, err)
	}
	if strings.Contains(got, "private source code") || strings.Contains(got, "secret") || !strings.Contains(got, "bug_report.md") || !strings.Contains(got, "robot+output+omitted") {
		t.Fatalf("URL contains agent output or is missing the redacted placeholder: %s", got)
	}
}

func TestIsTerminalUsesTTYDetection(t *testing.T) {
	want := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
	if got := isTerminal(os.Stdout); got != want {
		t.Fatalf("isTerminal = %v, want %v", got, want)
	}
}

func TestShouldUseUIDisablesTerminalDetection(t *testing.T) {
	if shouldUseUI(true, os.Stdout) {
		t.Fatal("shouldUseUI enabled the UI when disabled")
	}
	if got, want := shouldUseUI(false, os.Stdout), isTerminal(os.Stdout); got != want {
		t.Fatalf("shouldUseUI = %v, want %v", got, want)
	}
}

func TestTerminalUIReporterDoesNotWrapNilUI(t *testing.T) {
	var logs bytes.Buffer
	w := &Glorp{Out: &logs, UI: terminalUIReporter(nil)}

	w.logf("running without UI")

	if w.UI != nil {
		t.Fatal("terminalUIReporter returned a non-nil reporter for a nil UI")
	}
	if !strings.Contains(logs.String(), "running without UI") {
		t.Fatalf("log output = %q", logs.String())
	}
}

func TestProjectStateFingerprintIgnoresItemOrder(t *testing.T) {
	items := []projectItem{
		{ID: "a", Status: "Todo", Content: &projectContent{Issue: Issue{Number: 1, Repository: "o/r", State: "OPEN"}, Type: "Issue"}},
		{ID: "b", Status: "Done", Content: &projectContent{Issue: Issue{Number: 2, Repository: "o/r", State: "OPEN"}, Type: "Issue"}},
	}
	reordered := []projectItem{items[1], items[0]}
	if projectItemsFingerprint(items) != projectItemsFingerprint(reordered) {
		t.Fatal("reordered board items produced a different fingerprint")
	}
	moved := []projectItem{items[0], {ID: "b", Status: "Todo", Content: items[1].Content}}
	if projectItemsFingerprint(items) == projectItemsFingerprint(moved) {
		t.Fatal("moving a card between columns did not change the fingerprint")
	}
	added := append(append([]projectItem(nil), items...), projectItem{ID: "c", Status: "Todo", Content: &projectContent{Issue: Issue{Number: 3, Repository: "o/r", State: "OPEN"}, Type: "Issue"}})
	if projectItemsFingerprint(items) == projectItemsFingerprint(added) {
		t.Fatal("dragging a new issue onto the board did not change the fingerprint")
	}
}

func TestProjectStateQueriesOnlyBoardKeyFields(t *testing.T) {
	var calls [][]string
	gh := GHCLI{runCommand: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(`{"data":{"user":{"projectV2":{"items":{"nodes":[{"id":"PVTI_item","fieldValueByName":{"name":"Todo"},"content":{"__typename":"Issue","number":171,"state":"OPEN","repository":{"nameWithOwner":"lsegal/glorp"}}}],"pageInfo":{"hasNextPage":false,"endCursor":"cursor"}}}}}}`), nil
	}}
	state, err := gh.ProjectState(context.Background(), "https://github.com/users/lsegal/projects/3")
	if err != nil {
		t.Fatal(err)
	}
	if state == "" {
		t.Fatal("project state fingerprint is empty")
	}
	if len(calls) != 1 {
		t.Fatalf("gh calls = %#v, want a single request", calls)
	}
	query := calls[0][3]
	for _, unwanted := range []string{"body", "labels(", "createdAt", "title"} {
		if strings.Contains(query, unwanted) {
			t.Fatalf("probe query fetches %q, want only board key fields:\n%s", unwanted, query)
		}
	}
	if !strings.Contains(query, "number state repository{nameWithOwner}") {
		t.Fatalf("probe query missing board key fields:\n%s", query)
	}
}

func TestProjectStateRejectsRepositoryTarget(t *testing.T) {
	gh := GHCLI{runCommand: func(_ context.Context, _ ...string) ([]byte, error) {
		t.Fatal("repository target should not reach gh")
		return nil, nil
	}}
	if _, err := gh.ProjectState(context.Background(), "lsegal/glorp"); err == nil {
		t.Fatal("repository target did not produce an error")
	}
}

// The stub agent that writeFakeAgent installs is the test binary itself, so
// these variables carry its behaviour into the child process.
const (
	fakeAgentLogEnv          = "GLORP_FAKE_AGENT_LOG"
	fakeAgentResumeOutputEnv = "GLORP_FAKE_AGENT_RESUME_OUTPUT"
	fakeAgentResumeCodeEnv   = "GLORP_FAKE_AGENT_RESUME_CODE"
)

func TestMain(m *testing.M) {
	if log := os.Getenv(fakeAgentLogEnv); log != "" {
		os.Exit(runFakeAgent(log, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runFakeAgent records the invocation and answers a resume the way a dead
// agent session would, so RunSession's restart handling can be exercised on
// every platform.
func runFakeAgent(log string, args []string) int {
	file, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		file.Close()
		return 1
	}
	if _, err := fmt.Fprintf(file, "cwd=%s\n%s\n<<<END>>>\n", cwd, strings.Join(args, " ")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		file.Close()
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, arg := range args {
		if arg == "--resume" || arg == "resume" {
			fmt.Println(os.Getenv(fakeAgentResumeOutputEnv))
			code, err := strconv.Atoi(os.Getenv(fakeAgentResumeCodeEnv))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			return code
		}
	}
	fmt.Println("started")
	return 0
}
