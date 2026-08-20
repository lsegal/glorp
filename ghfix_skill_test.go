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
		"add the `agent-ready` label",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require follow-up routing %q", required)
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
