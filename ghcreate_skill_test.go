package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ghCreateSkill(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(".agents", "skills", "gh-create", "SKILL.md"))
	if err != nil {
		t.Fatalf("read gh-create skill: %v", err)
	}
	return string(data)
}

func TestGhCreateSkillIsBundledLikeTheOtherSkills(t *testing.T) {
	body := ghCreateSkill(t)
	if !strings.HasPrefix(body, "---\nname: gh-create\n") {
		t.Error("gh-create skill does not declare its name in front matter")
	}
	if _, err := os.Stat(filepath.Join(".agents", "skills", "gh-create", "agents", "openai.yaml")); err != nil {
		t.Errorf("gh-create skill has no agents/openai.yaml: %v", err)
	}
	if _, err := os.Stat(filepath.Join("site", "content", "skills", "gh-create.md")); err != nil {
		t.Errorf("gh-create skill has no site page: %v", err)
	}
	for _, script := range []string{"install.sh", "install.ps1"} {
		data, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("read %s: %v", script, err)
		}
		if !strings.Contains(string(data), `skills add "$repo@gh-create"`) {
			t.Errorf("%s does not install the gh-create skill", script)
		}
	}
}

func TestGhCreateQuotesTheOriginalPrompt(t *testing.T) {
	body := ghCreateSkill(t)
	for _, required := range []string{
		"End every issue with the user's original prompt, quoted verbatim",
		"<summary>Original prompt</summary>",
		"Quote the prompt exactly, typos and all",
		"`--body-file`",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-create skill does not require the quoted original prompt %q", required)
		}
	}
}

func TestGhCreateAppliesOnlyExistingLabels(t *testing.T) {
	body := ghCreateSkill(t)
	for _, required := range []string{
		"gh label list --repo OWNER/REPO",
		"Choose every label that matches the issue",
		"platform labels",
		"Never create a label, and never apply a label the repository does not define",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-create skill does not require label handling %q", required)
		}
	}
}

func TestGhCreateAssignsTheCurrentUserByDefault(t *testing.T) {
	body := ghCreateSkill(t)
	for _, required := range []string{
		"Assign the issue to the current `gh` user by default with `--assignee @me`",
		"When the user names a different assignee, assign that user instead",
		"When the user asks for no assignee, leave the issue unassigned",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-create skill does not require default assignment %q", required)
		}
	}
}

func TestGhCreateUploadsAndVerifiesMedia(t *testing.T) {
	body := ghCreateSkill(t)
	for _, required := range []string{
		"must be uploaded to GitHub and embedded in the issue",
		"Never leave a local file path",
		"--attach ./screenshots/settings.png",
		"https://uploads.github.com/user-attachments/assets",
		"Never commit media to the repository",
		"no local path, `file://` URL, home directory, or placeholder remains",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-create skill does not require media upload %q", required)
		}
	}
}

func TestGhCreateScrubsPrivateData(t *testing.T) {
	body := ghCreateSkill(t)
	for _, required := range []string{
		"gh repo view OWNER/REPO --json visibility",
		"Never mention a private repository, its name, its issues, or its code in an issue filed on a public repository",
		"Assume anything that came from a private repository is private",
		"local file paths",
		"email addresses",
		"access tokens",
		"replace each private value with `[redacted]`",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-create skill does not require privacy scrubbing %q", required)
		}
	}
}

func TestGhCreateResearchIsOptional(t *testing.T) {
	body := ghCreateSkill(t)
	for _, required := range []string{
		"it is never required to file, and it must not hold up filing",
		"search for related or duplicate issues",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gh-create skill does not keep research optional %q", required)
		}
	}
}
