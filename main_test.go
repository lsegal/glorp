package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"reflect"
	"slices"
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

func TestIssueListArgsUsesDefaultFilter(t *testing.T) {
	got := issueListArgs("owner/repo", defaultIssueFilter, false)
	want := []string{"issue", "list", "--repo", "owner/repo", "--state", "open", "--limit", "1000", "--search", "is:issue state:open author:@me", "--json", "number,title,body,state,createdAt,labels"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestIssueListArgsPreservesFilterSyntax(t *testing.T) {
	filter := "label=one status=closed label=two"
	got := issueListArgs("owner/repo", filter, false)
	if got[9] != filter {
		t.Fatalf("search query = %q, want %q", got[9], filter)
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

func TestIssueListArgsDisablesFilter(t *testing.T) {
	got := issueListArgs("owner/repo", "label:agent-ready", true)
	if slices.Contains(got, "--search") {
		t.Fatalf("all-issues args unexpectedly contain a filter: %#v", got)
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
