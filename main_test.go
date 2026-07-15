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
