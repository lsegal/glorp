package main

import (
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

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

func TestParseTargetURLs(t *testing.T) {
	for _, input := range []string{
		"https://github.com/lsegal/gh-watch",
		"https://github.com/lsegal/gh-watch/",
	} {
		got, err := parseTarget(input)
		if err != nil || got.repo != "lsegal/gh-watch" || got.isProject {
			t.Fatalf("parseTarget(%q) = %#v, %v", input, got, err)
		}
	}
	got, err := parseTarget("https://github.com/users/lsegal/projects/3")
	if err != nil || !got.isProject || got.owner != "lsegal" || got.projectID != "3" {
		t.Fatalf("project target = %#v, %v", got, err)
	}
}

func TestIssueRepositoryUsesProjectItemRepository(t *testing.T) {
	issue := Issue{Number: 32, Repository: "lsegal/gh-watch"}
	if got := issueRepository("https://github.com/users/lsegal/projects/3", issue); got != "lsegal/gh-watch" {
		t.Fatalf("issue repository = %q", got)
	}
}

func TestIssueRepositoryNormalizesRepositoryURL(t *testing.T) {
	issue := Issue{Number: 32}
	if got := issueRepository("https://github.com/lsegal/gh-watch", issue); got != "lsegal/gh-watch" {
		t.Fatalf("issue repository = %q", got)
	}
}

func TestProjectListArgs(t *testing.T) {
	got := projectListArgs(target{owner: "lsegal", projectID: "3", isProject: true}, "label=other status=closed", false)
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

func TestDecodeProjectIssues(t *testing.T) {
	got, err := decodeProjectIssues([]byte(`{"items":[{"status":"In Progress","content":{"number":7,"title":"bug","repository":"owner/repo","type":"Issue"}},{"content":{"number":8,"type":"PullRequest"}}]}`), nil)
	if err != nil || len(got) != 1 || got[0].Number != 7 || got[0].Repository != "owner/repo" || got[0].ProjectStatus != "In Progress" {
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
	got := issueListArgs("owner/repo", "label=agent-ready label=other status=closed", false)
	want := []string{"issue", "list", "--repo", "owner/repo", "--state", "open", "--limit", "1000", "--search", "label:agent-ready label:other status=closed", "--json", "number,title,body,state,createdAt,labels"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestSearchQueryParsesLabelTerms(t *testing.T) {
	got := searchQuery(" label=one   status=closed label=two ")
	if got != "label:one status=closed label:two" {
		t.Fatalf("search query = %q", got)
	}
}

func TestIssueListArgsDisablesFilter(t *testing.T) {
	got := issueListArgs("owner/repo", "label=agent-ready", true)
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
