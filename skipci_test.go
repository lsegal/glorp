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
		body := readWorkflow(t, name)
		if !strings.Contains(body, "on:\n  push:") && !strings.Contains(body, "\n  push:\n") {
			continue
		}
		if !strings.Contains(body, "!contains(github.event.head_commit.message, '[skip ci]')") {
			t.Errorf("workflow %s runs on push but does not skip [skip ci] commits", name)
		}
	}
}

// An empty closing commit, or one that only touches CHANGELOG.md, changes no
// non-markdown path, so a push-only CI workflow is skipped and the pull
// request's head SHA carries no verdict at all. The pull_request trigger closes
// that hole, and it must not be narrowed by a path filter of its own.
func TestCIRunsOnPullRequestsWithoutPathFilter(t *testing.T) {
	body := readWorkflow(t, "ci.yml")

	pr := triggerBlock(t, body, "pull_request")
	if pr == "" {
		t.Fatal("ci.yml must run on pull_request so every reviewable head SHA is checked")
	}
	for _, filter := range []string{"paths:", "paths-ignore:"} {
		if strings.Contains(pr, filter) {
			t.Errorf("pull_request trigger must not use %s; it would leave head SHAs unchecked", filter)
		}
	}
	if !strings.Contains(pr, "ready_for_review") {
		t.Error("pull_request trigger must include ready_for_review so a draft marked ready is checked")
	}
}

// Draft pull requests are where the gh-fix workflow parks its empty [skip ci]
// commit, so they must not trigger a build.
func TestCISkipsDraftPullRequests(t *testing.T) {
	body := readWorkflow(t, "ci.yml")
	guard := "github.event_name != 'pull_request' || github.event.pull_request.draft == false"
	jobs := strings.Count(body, "\n    runs-on:")
	if got := strings.Count(body, guard); got != jobs {
		t.Errorf("every one of the %d jobs must skip draft pull requests, found %d guards", jobs, got)
	}
}

// readWorkflow reads a workflow file with its line endings normalized, since a
// Windows checkout rewrites them to CRLF and would break the layout matching
// these tests rely on.
func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".github", "workflows", name))
	if err != nil {
		t.Fatalf("read workflow %s: %v", name, err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// triggerBlock returns the indented body of the named `on:` trigger.
func triggerBlock(t *testing.T, workflow, name string) string {
	t.Helper()
	marker := "\n  " + name + ":\n"
	start := strings.Index(workflow, marker)
	if start < 0 {
		return ""
	}
	rest := workflow[start+len(marker):]
	var block []string
	for _, line := range strings.Split(rest, "\n") {
		if line != "" && !strings.HasPrefix(line, "    ") {
			break
		}
		block = append(block, line)
	}
	return strings.Join(block, "\n")
}
