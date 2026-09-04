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
func TestClineDefinitionContract(t *testing.T) {
	agentContract{
		Definition: builtinDefinition(t, "cline"),
		Repo:       "o/r",
		Number:     7,
		Stdout:     "working on it",
		WantRun:    []string{"--auto-approve", "true", freshPrompt("o/r", 7)},
		WantResume: []string{"--auto-approve", "true", resumePrompt()},
		WantOutput: "working on it",
	}.check(t)
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
	for _, mode := range []agents.Mode{agents.ModeRun, agents.ModeResume, agents.ModeVision} {
		got := definition.Render(mode, agents.Values{Prompt: "do it", Model: "anthropic/claude-fable-5.1", Level: "high"})
		want := []string{"--auto-approve", "true", "--model", "anthropic/claude-fable-5.1", "--thinking", "high", "do it"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s argv = %#v, want %#v", mode, got, want)
		}
		bare := definition.Render(mode, agents.Values{Prompt: "do it"})
		if want := []string{"--auto-approve", "true", "do it"}; !reflect.DeepEqual(bare, want) {
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
