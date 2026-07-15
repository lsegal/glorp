package main

import (
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

func TestProjectListArgs(t *testing.T) {
	got := projectListArgs(target{owner: "lsegal", projectID: "3", isProject: true}, "label=agent-ready", false)
	want := []string{"project", "item-list", "3", "--owner", "lsegal", "--format", "json", "--limit", "1000", "--query", "is:issue is:open label:agent-ready"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestDecodeProjectIssues(t *testing.T) {
	got, err := decodeProjectIssues([]byte(`{"items":[{"content":{"number":7,"title":"bug","type":"Issue"}},{"content":{"number":8,"type":"PullRequest"}}]}`), nil)
	if err != nil || len(got) != 1 || got[0].Number != 7 {
		t.Fatalf("decode project issues = %#v, %v", got, err)
	}
}

func TestIssueListArgsUsesDefaultFilter(t *testing.T) {
	got := issueListArgs("owner/repo", "label=agent-ready", false)
	want := []string{"issue", "list", "--repo", "owner/repo", "--state", "open", "--limit", "1000", "--search", "label=agent-ready", "--json", "number,title,state,createdAt,labels"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestIssueListArgsDisablesFilter(t *testing.T) {
	got := issueListArgs("owner/repo", "label=agent-ready", true)
	if slices.Contains(got, "--search") {
		t.Fatalf("all-issues args unexpectedly contain a filter: %#v", got)
	}
}

func TestScrubRobotOutput(t *testing.T) {
	got := scrubRobotOutput("token=ghp_1234567890 secret:top-secret Authorization: Bearer abc.def\npublic")
	for _, leaked := range []string{"ghp_1234567890", "top-secret", "Bearer abc.def"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("output leaked %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "public") {
		t.Fatalf("unexpected scrubbed output: %s", got)
	}
}

func TestBugReportURL(t *testing.T) {
	got, err := bugReportURL("owner/repo", Issue{Number: 12}, []string{"agent", "--flag"}, "token=secret\nfailed")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil || u.Path != "/owner/repo/issues/new" {
		t.Fatalf("URL = %q, %v", got, err)
	}
	if strings.Contains(got, "secret") || !strings.Contains(got, "bug_report.md") {
		t.Fatalf("URL contains unsafe or missing data: %s", got)
	}
}
