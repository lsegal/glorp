package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentInstructionsForbidChangelogTests keeps the rule discoverable by the
// agents that write most of the tests here. The instruction only works if it is
// in the file their harness reads, so both AGENTS.md and the CLAUDE.md pointer
// at it have to stay in place.
func TestAgentInstructionsForbidChangelogTests(t *testing.T) {
	agents := readDoc(t, "AGENTS.md")
	for _, required := range []string{"CHANGELOG.md", "Never write a test"} {
		if !strings.Contains(agents, required) {
			t.Errorf("AGENTS.md does not mention %q", required)
		}
	}
	if claude := readDoc(t, "CLAUDE.md"); !strings.Contains(claude, "AGENTS.md") {
		t.Errorf("CLAUDE.md = %q, want it to point at AGENTS.md", claude)
	}
}

// TestNoTestReadsTheChangelog is the mechanical half of that instruction: a
// changelog assertion couples release prose to the test suite, so promoting or
// rewording an entry breaks CI for reasons unrelated to behavior. Comments may
// still name the file, and this file is exempt because it has to name it to
// forbid it; only code elsewhere that reaches for its contents is rejected.
func TestNoTestReadsTheChangelog(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test files found; the glob is wrong")
	}
	for _, file := range files {
		if file == "changelogtests_test.go" {
			continue // this file names the changelog to forbid it
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "CHANGELOG") {
				t.Errorf("%s:%d asserts on the changelog; test the behavior instead (see AGENTS.md)", file, i+1)
			}
		}
	}
}
