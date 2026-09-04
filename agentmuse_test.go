package main

import (
	"testing"

	"github.com/lsegal/glorp/agents"
)

// museStream is a captured `muse exec --json` stream, the sample recorded on
// #545 with the lifecycle events that surround it. Muse writes its whole
// message twice: once token by token on run.output.delta, and once complete on
// run.terminal.completed after the run is over. Decoding the deltas is what
// makes progress visible while the agent is still working, so the terminal
// events are ignored rather than printing the message a second time, and
// tool.result is ignored too because its payload.text is the tool's entire
// output rather than a summary of the call.
const museStream = `{"payload_type":"run.lifecycle.started","payload":{"session_id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301"}}\n` +
	`{"payload_type":"task.lifecycle.proposed","payload":{"task":{"kind":"tool.read_file"}}}\n` +
	`{"payload_type":"tool.result","payload":{"text":"1: hi"}}\n` +
	`{"payload_type":"run.output.delta","payload":{"text":"a.txt "}}\n` +
	`{"payload_type":"run.output.delta","payload":{"text":"contains "}}\n` +
	`{"payload_type":"run.output.delta","payload":{"text":"hi"}}\n` +
	`{"payload_type":"run.terminal.completed","payload":{"text":"a.txt contains hi"}}`

// museDecoded is museStream read through the definition: one sentence rather
// than the three lines an event-per-line decoder would write, and no second
// copy of it from the terminal event.
const museDecoded = "a.txt contains hi"

// TestMuseDefinitionContract proves the shipped Meta Muse Code definition
// against the fake CLI. Muse takes a caller-assigned session ID like Claude
// does, so a resume is the same `muse exec` invocation carrying the ID glorp
// already holds, and its
// JSONL stream is decoded by the field paths its own definition names.
func TestMuseDefinitionContract(t *testing.T) {
	agentContract{
		Definition: builtinDefinition(t, "muse"),
		Repo:       "o/r",
		Number:     7,
		SessionID:  "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		Stdout:     museStream,
		WantRun: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--approval-mode", "never", "--user-input-auto-resolve", "--json", freshPrompt("o/r", 7),
		},
		WantResume: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--approval-mode", "never", "--user-input-auto-resolve", "--json", resumePrompt(),
		},
		WantOutput: museDecoded,
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
		Stdout:     museStream,
		WantRun: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--yolo", "--user-input-auto-resolve", "--json", freshPrompt("o/r", 7),
		},
		WantResume: []string{
			"exec", "--session-id", "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			"--yolo", "--user-input-auto-resolve", "--json", resumePrompt(),
		},
		WantOutput: museDecoded,
	}.check(t)
}

// TestMuseProgressCarriesNoToolCalls records the one thing the Muse stream
// still cannot give the dashboard, so it is not re-investigated on every pass
// over the definition. No event pairs a tool name with the input it was called
// with: the name appears alone on task.lifecycle.proposed as a task kind
// shared with model turns ("tool.read_file" beside "model.meta.response"), and
// on tool.result beside payload.text, which is the tool's entire output rather
// than a summary of the call. Muse progress therefore carries text but no
// "Running: <tool> <detail>" line until Muse's stream gains a call event or
// the decoder can map fields per event type, tracked as its own issue. Nothing
// here is special-cased for Muse in Go.
func TestMuseProgressCarriesNoToolCalls(t *testing.T) {
	definition := builtinDefinition(t, "muse")
	if definition.Output.JSONL.ToolName != "" || definition.Output.JSONL.ToolInput != "" {
		t.Fatal("muse names a tool-call path, but no event in its stream pairs a tool name with its input")
	}
	// The vision call reads a board screenshot and its answer is parsed as
	// prose, so that mode alone stays on Muse's plain output.
	for _, arg := range definition.Render(agents.ModeVision, agents.Values{Prompt: "do it", Image: "/tmp/shot.png"}) {
		if arg == "--json" {
			t.Fatal("the vision argv asks for --json, but its answer is read as prose")
		}
	}
	for _, mode := range []agents.Mode{agents.ModeRun, agents.ModeResume} {
		asked := false
		for _, arg := range definition.Render(mode, agents.Values{Prompt: "do it"}) {
			asked = asked || arg == "--json"
		}
		if !asked {
			t.Fatalf("%s argv does not ask for --json, but its output is decoded as a JSON stream", mode)
		}
	}
}
