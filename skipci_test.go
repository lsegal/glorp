package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gh-fix skill opens every draft pull request with an empty commit on a
// tree identical to the default branch. That commit must be marked [skip ci]
// so the push does not burn a full CI run.
func TestGhFixInitialCommitSkipsCI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".agents", "skills", "gh-fix", "SKILL.md"))
	if err != nil {
		t.Fatalf("read gh-fix skill: %v", err)
	}
	var line string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, "Create an empty initial commit") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("gh-fix skill no longer documents the empty initial commit")
	}
	if !strings.Contains(line, "Start work on issue #<ISSUENUMBER> [skip ci]") {
		t.Errorf("initial commit message must carry the [skip ci] marker, got: %s", line)
	}
}

// Push-triggered workflows must honor the [skip ci] marker instead of building
// commits that explicitly opt out.
func TestPushWorkflowsHonorSkipCI(t *testing.T) {
	for _, name := range []string{"ci.yml", "pages.yml"} {
		data, err := os.ReadFile(filepath.Join(".github", "workflows", name))
		if err != nil {
			t.Fatalf("read workflow %s: %v", name, err)
		}
		body := string(data)
		if !strings.Contains(body, "on:\n  push:") && !strings.Contains(body, "\n  push:\n") {
			continue
		}
		if !strings.Contains(body, "!contains(github.event.head_commit.message, '[skip ci]')") {
			t.Errorf("workflow %s runs on push but does not skip [skip ci] commits", name)
		}
	}
}
