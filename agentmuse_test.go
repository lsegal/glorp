package main

import "testing"

// TestMuseDefinitionContract proves the shipped Meta Muse Code definition
// against the fake CLI. Muse takes a caller-assigned session ID like Claude
// does, so a resume is the same `muse exec` invocation carrying the ID glorp
// already holds, and its plain output is shown as it is written.
func TestMuseDefinitionContract(t *testing.T) {
	agentContract{
		Definition: builtinDefinition(t, "muse"),
		Repo:       "o/r",
		Number:     7,
		SessionID:  "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		Stdout:     "working on it",
		WantRun: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--approval-mode", "never", "--user-input-auto-resolve", freshPrompt("o/r", 7),
		},
		WantResume: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--approval-mode", "never", "--user-input-auto-resolve", resumePrompt(),
		},
		WantOutput: "working on it",
	}.check(t)
}

// TestMuseYoloDefinitionContract proves the yolo arm swaps the approval mode
// for Muse's own bypass rather than passing both.
func TestMuseYoloDefinitionContract(t *testing.T) {
	agentContract{
		Definition: builtinDefinition(t, "muse"),
		Repo:       "o/r",
		Number:     7,
		SessionID:  "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		Yolo:       true,
		Stdout:     "working on it",
		WantRun: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--yolo", "--user-input-auto-resolve", freshPrompt("o/r", 7),
		},
		WantResume: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--yolo", "--user-input-auto-resolve", resumePrompt(),
		},
		WantOutput: "working on it",
	}.check(t)
}
