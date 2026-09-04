package main

import (
	"reflect"
	"testing"

	"github.com/lsegal/glorp/agents"
)

// TestClineDefinitionContract proves the shipped cline definition against the
// fake CLI. Cline is the first agent glorp ships that has no headless resume:
// `cline --id <session>` switches the CLI into its interactive TUI even when a
// prompt is passed positionally, and refuses to run without a terminal, so
// there is no session for glorp to assign or to read back. The definition says
// so with "session": {"assign": "none"}, and its resume template restarts the
// work with the recovery prompt rather than naming a session, which is what
// this contract checks: the run and the resume render the same argv apart from
// the prompt, and a resume the agent cannot honour still restarts instead of
// failing the job.
//
// The stdout below is a captured `cline --json` run, trimmed of its usage and
// token-level events. Cline wraps every event in its own envelope, so the type
// is at event.type and the text at event.text, and a finished tool call is a
// content_end whose contentType is "tool": glorp reads the name out of it and
// renders the same "Running: ..." line Claude's decoder does.
func TestClineDefinitionContract(t *testing.T) {
	agentContract{
		Definition: builtinDefinition(t, "cline"),
		Repo:       "o/r",
		Number:     7,
		Stdout: `{"ts":"2026-09-04T00:52:26.301Z","type":"agent_event","event":{"type":"content_start","contentType":"text","text":"reading"}}\n` +
			`{"ts":"2026-09-04T00:52:26.297Z","type":"agent_event","event":{"type":"usage","inputTokens":3818,"outputTokens":91}}\n` +
			`{"ts":"2026-09-04T00:52:26.301Z","type":"agent_event","event":{"type":"content_end","contentType":"text","text":"reading the file"}}\n` +
			`{"ts":"2026-09-04T00:52:26.304Z","type":"hook_event","hookEventName":"tool_call","agentId":"agent_1","taskId":"conv_1"}\n` +
			`{"ts":"2026-09-04T00:52:26.311Z","type":"agent_event","event":{"type":"content_end","contentType":"tool","toolCallId":"call_1","toolName":"read_files","output":[{"query":"a.txt","result":"1 | hi","success":true}]}}\n` +
			`{"ts":"2026-09-04T00:52:27.743Z","type":"agent_event","event":{"type":"done","reason":"completed","text":"reading the file","iterations":2}}`,
		WantRun:    []string{"--auto-approve", "true", "--json", freshPrompt("o/r", 7)},
		WantResume: []string{"--auto-approve", "true", "--json", resumePrompt()},
		WantOutput: "reading the file\nRunning: read_files",
	}.check(t)
}

// TestClineDefinitionDropsTheStreamsDuplicateText records why the ignore list
// is as long as it is. Cline prints every assistant message three times over:
// once token by token as content_start, once whole as content_end, and once
// more in the done event that ends the turn. Only content_end is read, so the
// dashboard shows the message once rather than once per token plus twice more.
func TestClineDefinitionDropsTheStreamsDuplicateText(t *testing.T) {
	jsonl := builtinDefinition(t, "cline").Output.JSONL
	if jsonl == nil {
		t.Fatal("cline no longer describes its JSONL envelope")
	}
	ignored := map[string]bool{}
	for _, event := range jsonl.Ignore {
		ignored[event] = true
	}
	for _, event := range []string{"content_start", "done"} {
		if !ignored[event] {
			t.Fatalf("event %q is decoded, but it repeats text content_end already carried", event)
		}
	}
	if jsonl.ToolInput != "" {
		t.Fatalf("output.jsonl.toolInput = %q, but cline reports no tool arguments", jsonl.ToolInput)
	}
}

// TestClineDefinitionDeclaresNoSession records the finding the contract above
// rests on. An agent whose session assignment silently changed to "glorp"
// would have glorp generate an ID it then never passes to anything, and one
// changed to "capture" would have it watch the output forever for an ID the
// CLI does not print, so the declaration is asserted rather than assumed.
func TestClineDefinitionDeclaresNoSession(t *testing.T) {
	definition := builtinDefinition(t, "cline")
	if definition.Session.Assign != agents.AssignNone {
		t.Fatalf("session.assign = %q, want %q: cline has no headless resume", definition.Session.Assign, agents.AssignNone)
	}
	if definition.AssignsSessionID() || definition.CapturesSessionID() {
		t.Fatal("cline neither takes a session ID from glorp nor prints one of its own")
	}
	if !definition.Supports(agents.ModeResume) {
		t.Fatal("a resume template is still required, so a recovered run restarts rather than rendering nothing")
	}
}

// TestClineDefinitionRendersModelAndLevel checks the flags the CLI actually
// takes are the ones rendered: -m/--model for the model and --thinking for the
// reasoning effort, both omitted entirely when the spec names neither, since
// cline falls back to the provider default rather than to a value of its own.
func TestClineDefinitionRendersModelAndLevel(t *testing.T) {
	definition := builtinDefinition(t, "cline")
	// The one-shot vision read stays on plain text, exactly as Claude's does:
	// its answer is parsed as prose rather than shown as progress, so wrapping
	// it in cline's event envelope would buy nothing.
	for mode, stream := range map[agents.Mode][]string{
		agents.ModeRun:    {"--json"},
		agents.ModeResume: {"--json"},
		agents.ModeVision: nil,
	} {
		got := definition.Render(mode, agents.Values{Prompt: "do it", Model: "anthropic/claude-fable-5.1", Level: "high"})
		want := append([]string{"--auto-approve", "true", "--model", "anthropic/claude-fable-5.1", "--thinking", "high"}, stream...)
		if want = append(want, "do it"); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s argv = %#v, want %#v", mode, got, want)
		}
		bare := definition.Render(mode, agents.Values{Prompt: "do it"})
		want = append(append([]string{"--auto-approve", "true"}, stream...), "do it")
		if !reflect.DeepEqual(bare, want) {
			t.Fatalf("%s argv without a model or level = %#v, want %#v", mode, bare, want)
		}
	}
}

// TestClineDefinitionAutoApprovesRegardlessOfYolo records why --yolo renders
// nothing extra. Cline has no sandbox and no lesser autonomous mode: its only
// approval control is --auto-approve, which defaults to true and which a run
// left at false cannot get past headlessly, because there is no terminal to
// approve on. Passing it explicitly in every mode keeps a user's own
// configuration from turning a dispatched run into one that never proceeds.
func TestClineDefinitionAutoApprovesRegardlessOfYolo(t *testing.T) {
	definition := builtinDefinition(t, "cline")
	for _, mode := range []agents.Mode{agents.ModeRun, agents.ModeResume, agents.ModeVision} {
		safe := definition.Render(mode, agents.Values{Prompt: "do it"})
		yolo := definition.Render(mode, agents.Values{Prompt: "do it", Yolo: true})
		if !reflect.DeepEqual(safe, yolo) {
			t.Fatalf("%s argv differs on --yolo: %#v vs %#v", mode, safe, yolo)
		}
		if want := []string{"--auto-approve", "true"}; !reflect.DeepEqual(safe[:2], want) {
			t.Fatalf("%s argv = %#v, want it to start with %#v", mode, safe, want)
		}
	}
}

// TestClineLevelsMatchTheCLI checks the level allow-list is the set cline's
// --thinking accepts, so a value it would reject is answered by glorp with the
// list rather than by cline with a usage error one dispatch later.
func TestClineLevelsMatchTheCLI(t *testing.T) {
	definition := builtinDefinition(t, "cline")
	for _, level := range []string{"none", "low", "medium", "high", "xhigh"} {
		if !definition.AcceptsLevel(level) {
			t.Fatalf("level %q was rejected, but cline --thinking accepts it", level)
		}
	}
	if definition.AcceptsLevel("max") {
		t.Fatal(`level "max" was accepted, but cline --thinking does not take it`)
	}
	if _, err := parseAgentSpecIn(agents.MustBuiltin(), "cline/anthropic/claude-fable-5.1:xhigh"); err != nil {
		t.Fatalf("--agent cline/anthropic/claude-fable-5.1:xhigh was rejected: %v", err)
	}
}
