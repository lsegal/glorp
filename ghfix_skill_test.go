package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ghFixSkill(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".agents", "skills", "gh-fix", "SKILL.md"))
	if err != nil {
		t.Fatalf("read gh-fix skill: %v", err)
	}
	return string(data)
}

func TestGhFixTracksFollowUpsBeforeStopping(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"immediately before stopping a failed or stalled run",
		"whether or not a pull request exists",
		"Do not wait for a merge",
		"when one exists, the pull request",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require blocked-run follow-up behavior %q", required)
		}
	}
}

func TestGhFixRoutesFollowUpsForGlorp(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"set each new item's Status to that project's `Todo` option",
		"Match `Todo` case-insensitively",
		"record whether the invocation directory contains `.glorp.json`",
		"Use the presence recorded before cloning",
		"--add-assignee @me",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require follow-up routing %q", required)
		}
	}
}

func TestGhFixMakesDependentFollowUpsSubIssues(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"unresolved issue dependency",
		"make the follow-up a sub-issue of that related blocking issue",
		"GitHub's sub-issue API",
		"specific blocker it addresses",
		"rather than guessing when the relationship is ambiguous",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require dependent follow-up sub-issue routing %q", required)
		}
	}
}

func TestGhFixPrefersSquashMerges(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"prefer them in this order: squash, then merge, then rebase",
		"use the PR's title and body as the squash commit message rather than overriding it",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not document squash-first merge order %q", required)
		}
	}
}

func TestGhFixNeverReusesClosedPullRequest(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"Never reuse a CLOSED pull request, whether or not it was merged",
		"only an OPEN pull request is eligible to resume, regardless of which signal matched it",
		"otherwise proceed as if none was found, even if a closed one matched",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not forbid reusing a closed pull request %q", required)
		}
	}
}

func TestGhFixTreatsIdentityMentionsAsThreadedInstructions(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"identity:/glorp:<ID>",
		"threaded conversation",
		"chronological order",
		"direct instructions",
		"must not be ignored",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not document identity mention behavior %q", required)
		}
	}
}
