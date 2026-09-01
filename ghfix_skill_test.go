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

func TestGhFixCopiesOriginatingIssueMetadataToFollowUps(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"Carry the originating issue's milestone and assignees onto every new follow-up issue unchanged",
		"say which value was skipped rather than dropping the whole step",
		"plus the inherited milestone and assignees, to a reused issue only where it carries none of its own for that field",
		"copy the originating issue's milestone or assignees",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require follow-up metadata inheritance %q", required)
		}
	}
}

func TestGhFixLabelsFollowUpsFromTheFollowUpItself(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"Build every new follow-up issue's labels from the follow-up itself rather than copying the originating issue's label set",
		"`gh label list`",
		"a defect gets whatever this repository calls a bug",
		"new work gets whatever it calls a feature",
		"never invent a label it does not define",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require follow-up labels chosen from the follow-up %q", required)
		}
	}
}

func TestGhFixCarriesOverOnlyStillRelevantLabels(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"Carry an originating label over only when it is still accurate for the follow-up",
		"an area, a component, a release train, a priority",
		"its triage or workflow state",
		"A carried label never overrides the kind label chosen for the follow-up",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not restrict originating label carry-over %q", required)
		}
	}
}

func TestGhFixSplitsSeparableIssuesConservatively(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"## Split a multi-part issue into sub-issues",
		"Split only when the issue itself plainly states multiple distinct, separable tasks",
		"Be very conservative",
		"could be implemented, tested, and merged on its own without the others",
		"when the division is your own inference rather than the issue's own words",
		"When in doubt, do not split",
		"attach it to the originating issue with GitHub's sub-issue API",
		"Do not settle for a Markdown checklist",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not require conservative sub-issue splitting %q", required)
		}
	}
}

func TestGhFixTreatsParentIssuesAsTrackingIssues(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"the parent is a tracking or meta issue",
		"It does not require a pull request, code, or a changelog entry to close",
		"it must not consume an agent slot",
		"end the run without cloning, branching, or opening a pull request",
		"the next agent slot is free to pick up a sub-issue",
		"Route every new sub-issue exactly as \"Create follow-up issues\" routes a follow-up",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not treat split parents as tracking issues %q", required)
		}
	}
}

func TestGhFixNeverResplitsAnIssueWithSubIssues(t *testing.T) {
	body := ghFixSkill(t)
	for _, required := range []string{
		"Read the issue's existing sub-issues first",
		"Never create further sub-issues for it and never open a pull request for it",
		"the only thing `/gh-fix` does with such an issue is determine whether it is closeable",
		"close the parent with a comment naming the completed sub-issues",
		"end the run reporting which ones remain, and post no comment",
		"repeated dispatches do not accumulate identical comments",
		"is exempt from this rule",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-fix skill does not stay re-entrant on already-split issues %q", required)
		}
	}
}
