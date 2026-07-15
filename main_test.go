package main

import (
	"reflect"
	"slices"
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
	want := []string{"issue", "list", "--repo", "owner/repo", "--state", "open", "--limit", "1000", "--search", "label=agent-ready", "--json", "number,title,state,createdAt"}
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
