package agent

import (
	"reflect"
	"strings"
	"testing"
)

// TestBuiltinsLoadAndValidate checks the embedded definitions are the ones
// glorp actually ships with, since a definition that fails to load leaves the
// agent looking like a typo in --agent.
func TestBuiltinsLoadAndValidate(t *testing.T) {
	registry, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins() error = %v", err)
	}
	if got, want := registry.Names(), []string{"claude", "codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("built-in agents = %#v, want %#v", got, want)
	}
	codex, _ := registry.Lookup("codex")
	if codex.SessionPattern() == nil {
		t.Fatal("codex reports its session ID on stdout but has no compiled capture pattern")
	}
	if codex.Session.Assign {
		t.Fatal("codex assigns its own session IDs; glorp must not hand it one")
	}
	claude, _ := registry.Lookup("claude")
	if !claude.Session.Assign || claude.SessionPattern() != nil {
		t.Fatalf("claude takes a caller-provided session ID: %#v", claude.Session)
	}
	if got, want := claude.EnvPairs(), []string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claude env = %#v, want %#v", got, want)
	}
	if got, want := codex.EnvPairs(), []string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("codex env = %#v, want %#v", got, want)
	}
}

// TestRenderArgsMatchesShippedInvocations pins the argv every mode produces for
// the two built-in agents against what glorp sent before agents were described
// by definitions. It is the parity claim of issue #487, so the expectations are
// written out literally rather than derived from the definitions.
func TestRenderArgsMatchesShippedInvocations(t *testing.T) {
	registry, err := Builtins()
	if err != nil {
		t.Fatalf("Builtins() error = %v", err)
	}
	for _, test := range []struct {
		name   string
		agent  string
		mode   string
		values Values
		want   []string
	}{
		{
			name:   "codex fresh run",
			agent:  "codex",
			mode:   ModeRun,
			values: Values{Prompt: "do it"},
			want:   []string{"exec", "do it"},
		},
		{
			name:   "codex fresh run with model and level",
			agent:  "codex",
			mode:   ModeRun,
			values: Values{Prompt: "do it", Model: "gpt-5.6-luna", Level: "high"},
			want:   []string{"exec", "--model", "gpt-5.6-luna", "-c", "model_reasoning_effort=high", "do it"},
		},
		{
			name:   "codex fresh run in yolo mode",
			agent:  "codex",
			mode:   ModeRun,
			values: Values{Prompt: "do it", Yolo: true},
			want:   []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "do it"},
		},
		{
			// A codex session ID is captured off stdout, so a fresh run never
			// names one even when glorp is holding one from an earlier run.
			name:   "codex fresh run ignores a recorded session",
			agent:  "codex",
			mode:   ModeRun,
			values: Values{Prompt: "do it", Session: "session-7"},
			want:   []string{"exec", "do it"},
		},
		{
			name:   "codex resume",
			agent:  "codex",
			mode:   ModeResume,
			values: Values{Prompt: "continue", Session: "session-7", Yolo: true},
			want:   []string{"exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "session-7", "continue"},
		},
		{
			name:   "codex vision",
			agent:  "codex",
			mode:   ModeVision,
			values: Values{Prompt: "read it", Image: "/tmp/shot.png", Model: "gpt-5"},
			want:   []string{"exec", "--image", "/tmp/shot.png", "--model", "gpt-5", "read it"},
		},
		{
			name:   "claude fresh run",
			agent:  "claude",
			mode:   ModeRun,
			values: Values{Prompt: "do it"},
			want:   []string{"-p", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", "do it"},
		},
		{
			name:   "claude fresh run with an assigned session",
			agent:  "claude",
			mode:   ModeRun,
			values: Values{Prompt: "do it", Session: "session-12"},
			want:   []string{"-p", "--session-id", "session-12", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", "do it"},
		},
		{
			name:   "claude fresh run with model and level",
			agent:  "claude",
			mode:   ModeRun,
			values: Values{Prompt: "do it", Model: "claude-sonnet", Level: "medium"},
			want:   []string{"-p", "--permission-mode", "auto", "--model", "claude-sonnet", "--effort", "medium", "--output-format", "stream-json", "--verbose", "do it"},
		},
		{
			name:  "claude fresh run with remote control",
			agent: "claude",
			mode:  ModeRun,
			values: Values{
				Prompt: "do it", Yolo: true, RemoteControl: true,
				RemoteControlSettings: `{"remoteControlAtStartup":true}`, RemoteControlName: "glorp owner/repo#12",
			},
			want: []string{"-p", "--dangerously-skip-permissions", "--settings", `{"remoteControlAtStartup":true}`, "--rc", "glorp owner/repo#12", "--output-format", "stream-json", "--verbose", "do it"},
		},
		{
			name:   "claude resume",
			agent:  "claude",
			mode:   ModeResume,
			values: Values{Prompt: "continue", Session: "session-7", Yolo: true},
			want:   []string{"-p", "--resume", "session-7", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose", "continue"},
		},
		{
			name:   "claude vision",
			agent:  "claude",
			mode:   ModeVision,
			values: Values{Prompt: "read it", Image: "/tmp/shot.png"},
			want:   []string{"-p", "--permission-mode", "auto", "read it"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition, ok := registry.Lookup(test.agent)
			if !ok {
				t.Fatalf("no definition for %q", test.agent)
			}
			if got := definition.RenderArgs(test.mode, test.values); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("RenderArgs(%q) = %#v, want %#v", test.mode, got, test.want)
			}
		})
	}
}

// TestRenderArgsWithoutTemplateRendersNothing covers an agent that declares no
// invocation for a mode, which is how a CLI with no one-shot vision call says
// so rather than being handed argv meant for another agent.
func TestRenderArgsWithoutTemplateRendersNothing(t *testing.T) {
	definition := &Definition{Args: ArgTemplates{Run: []Fragment{{Args: []string{"-p"}}}}}
	if got := definition.RenderArgs(ModeVision, Values{Prompt: "read it"}); got != nil {
		t.Fatalf("vision args = %#v, want none", got)
	}
}

func TestValidateNamesTheOffendingField(t *testing.T) {
	valid := `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true}}`
	for _, test := range []struct {
		name string
		json string
		want string
	}{
		{"unknown placeholder", `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prmopt}"]}]},"session":{"assign":true}}`, `field "args.run[0]"`},
		{"unknown condition", `{"name":"muse","binary":"muse","args":{"run":[{"args":["-x"],"when":"yodo"}]},"session":{"assign":true}}`, `unknown condition "yodo"`},
		{"empty name", `{"binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true}}`, `empty "name" field`},
		{"no binary", `{"name":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true}}`, `field "binary"`},
		{"no run argv", `{"name":"muse","binary":"muse","session":{"assign":true}}`, `field "args.run"`},
		{"empty fragment", `{"name":"muse","binary":"muse","args":{"run":[{"args":[]}]},"session":{"assign":true}}`, `field "args.run[0]"`},
		{"no session source", `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prompt}"]}]}}`, `field "session"`},
		{"two session sources", `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true,"capturePattern":"id: (.+)"}}`, `mutually exclusive`},
		{"capture pattern without a group", `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"capturePattern":"id: .+"}}`, `field "session.capturePattern"`},
		{"unparseable capture pattern", `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"capturePattern":"id: ("}}`, `field "session.capturePattern"`},
		{"unknown output format", `{"name":"muse","binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true},"output":{"format":"yaml"}}`, `field "output.format"`},
		{"name with a spec separator", `{"name":"mu/se","binary":"muse","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true}}`, `field "name"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{definitions: map[string]*Definition{}}
			err := registry.Apply("agents.json", []byte(`{"agents":[`+test.json+`]}`))
			if err == nil {
				t.Fatal("invalid definition was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
			if !strings.Contains(err.Error(), "agents.json") {
				t.Fatalf("error = %v, want it to name the file", err)
			}
		})
	}
	registry := &Registry{definitions: map[string]*Definition{}}
	if err := registry.Apply("agents.json", []byte(`{"agents":[`+valid+`]}`)); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
}

func TestAllowLists(t *testing.T) {
	definition := &Definition{Levels: []string{"low", "high"}, Models: []string{"muse-1"}}
	for _, test := range []struct {
		level, model string
		want         bool
	}{
		{"", "", true},
		{"low", "muse-1", true},
		{"medium", "muse-1", false},
		{"low", "muse-2", false},
	} {
		if got := definition.AllowsLevel(test.level) && definition.AllowsModel(test.model); got != test.want {
			t.Fatalf("allows(%q, %q) = %t, want %t", test.level, test.model, got, test.want)
		}
	}
	// An unrestricted definition accepts anything, so a new agent only has to
	// name its levels when it wants the others rejected up front.
	open := &Definition{}
	if !open.AllowsLevel("turbo") || !open.AllowsModel("anything") {
		t.Fatal("a definition with no allow-list rejected a value")
	}
}
