package main

import (
	"testing"

	"github.com/lsegal/glorp/agents"
)

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

// TestMuseDefinitionStaysOnPlainText records why Muse is the one agent of the
// three surveyed for #536 that keeps "output": {"format": "text"}, so the
// question is not reopened every time the generic decoder gains an agent.
//
// `muse exec --json` does emit a rich JSONL stream, but nothing in it is
// usable by the decoder as it stands:
//
//   - Its only incremental text event, run.output.delta, carries a token-sized
//     fragment ("a.txt ", "contains ", "hi"). The decoder writes one line per
//     event, so decoding those deltas breaks a sentence across a line each,
//     which is worse than the plain output it would replace.
//   - The whole message arrives once more on run.terminal.completed, but that
//     is the same text plain mode already prints, and only once the run is
//     over -- exactly the "nothing until the agent finishes" #536 set out to
//     fix.
//   - No event pairs a tool name with the input it was called with. The name
//     appears alone on task.lifecycle.proposed as a task kind
//     ("tool.read_file", alongside "model.meta.response" for model turns), and
//     on tool.result beside payload.text, which is the tool's entire output
//     rather than a summary of the call.
//
// What the decoder is missing is a way for a definition to say that an event's
// text is a delta to be joined rather than a whole line, tracked as its own
// issue. Nothing here is special-cased for Muse in Go.
func TestMuseDefinitionStaysOnPlainText(t *testing.T) {
	definition := builtinDefinition(t, "muse")
	if got := definition.Output.Decoder(); got != agents.FormatText {
		t.Fatalf("output decoder = %q, want %q until the decoder can join text deltas", got, agents.FormatText)
	}
	if definition.Output.JSONL != nil {
		t.Fatal("muse describes a JSONL envelope it does not ask the CLI for")
	}
	for _, mode := range []agents.Mode{agents.ModeRun, agents.ModeResume, agents.ModeVision} {
		for _, arg := range definition.Render(mode, agents.Values{Prompt: "do it"}) {
			if arg == "--json" {
				t.Fatalf("%s argv asks for --json, but the output is decoded as plain text", mode)
			}
		}
	}
}
