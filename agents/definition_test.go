package agents

import (
	"reflect"
	"strings"
	"testing"
)

// TestBuiltinDefinitionsLoad checks the embedded documents parse and validate.
// They are the only definitions a default run has, so a broken one is a glorp
// that cannot dispatch anything.
func TestBuiltinDefinitionsLoad(t *testing.T) {
	registry, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin() error = %v", err)
	}
	if got, want := registry.Names(), []string{"claude", "codex", "muse"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("built-in agents = %v, want %v", got, want)
	}
	for _, name := range registry.Names() {
		definition, _ := registry.Lookup(name)
		for _, mode := range []Mode{ModeRun, ModeResume, ModeVision} {
			if !definition.Supports(mode) {
				t.Errorf("%s declares no %q template", name, mode)
			}
		}
	}
}

// TestBuiltinArgvIsUnchanged pins the argv every mode renders for the agents
// glorp already shipped. This is the parity claim the refactor rests on: the
// definitions replace hand-written branches, so the arguments they produce
// have to be exactly what those branches produced.
func TestBuiltinArgvIsUnchanged(t *testing.T) {
	registry := MustBuiltin()
	prompt := "/gh-fix o/r#7"
	for _, test := range []struct {
		name   string
		agent  string
		mode   Mode
		values Values
		want   []string
	}{
		{
			name: "codex fresh", agent: "codex", mode: ModeRun,
			values: Values{Prompt: prompt},
			want:   []string{"exec", prompt},
		},
		{
			name: "codex fresh with model and level", agent: "codex", mode: ModeRun,
			values: Values{Prompt: prompt, Model: "gpt-5.6-luna", Level: "high"},
			want:   []string{"exec", "--model", "gpt-5.6-luna", "-c", "model_reasoning_effort=high", prompt},
		},
		{
			name: "codex fresh yolo", agent: "codex", mode: ModeRun,
			values: Values{Prompt: prompt, Yolo: true},
			want:   []string{"exec", "--dangerously-bypass-approvals-and-sandbox", prompt},
		},
		{
			// A resume takes neither the model nor the level: the session it
			// continues already has them.
			name: "codex resume", agent: "codex", mode: ModeResume,
			values: Values{Prompt: prompt, Session: "sess-1", Model: "gpt-5.6-luna", Level: "high"},
			want:   []string{"exec", "resume", "sess-1", prompt},
		},
		{
			name: "codex vision", agent: "codex", mode: ModeVision,
			values: Values{Prompt: prompt, Image: "/tmp/shot.png", Model: "gpt-5"},
			want:   []string{"exec", "--image", "/tmp/shot.png", "--model", "gpt-5", prompt},
		},
		{
			name: "claude fresh", agent: "claude", mode: ModeRun,
			values: Values{Prompt: prompt},
			want:   []string{"-p", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", prompt},
		},
		{
			name: "claude fresh with session, model, and level", agent: "claude", mode: ModeRun,
			values: Values{Prompt: prompt, Session: "session-12", Model: "opus", Level: "low"},
			want: []string{
				"-p", "--session-id", "session-12", "--permission-mode", "auto",
				"--model", "opus", "--effort", "low", "--output-format", "stream-json", "--verbose", prompt,
			},
		},
		{
			name: "claude fresh yolo", agent: "claude", mode: ModeRun,
			values: Values{Prompt: prompt, Yolo: true},
			want:   []string{"-p", "--dangerously-skip-permissions", "--output-format", "stream-json", "--verbose", prompt},
		},
		{
			name: "claude fresh with remote control", agent: "claude", mode: ModeRun,
			values: Values{Prompt: prompt, RemoteControl: true, Settings: `{"remoteControlAtStartup":true}`, SessionName: "glorp o/r#7"},
			want: []string{
				"-p", "--permission-mode", "auto", "--settings", `{"remoteControlAtStartup":true}`,
				"--rc", "glorp o/r#7", "--output-format", "stream-json", "--verbose", prompt,
			},
		},
		{
			// Codex declares no remote-control fragment, so the run's flag
			// reaches its argv not at all.
			name: "codex ignores remote control", agent: "codex", mode: ModeRun,
			values: Values{Prompt: prompt, RemoteControl: true, Settings: `{"remoteControlAtStartup":true}`, SessionName: "glorp o/r#7"},
			want:   []string{"exec", prompt},
		},
		{
			name: "claude resume", agent: "claude", mode: ModeResume,
			values: Values{Prompt: prompt, Session: "session-12", Model: "opus", Level: "low"},
			want: []string{
				"-p", "--resume", "session-12", "--permission-mode", "auto",
				"--output-format", "stream-json", "--verbose", prompt,
			},
		},
		{
			name: "claude vision", agent: "claude", mode: ModeVision,
			values: Values{Prompt: prompt, Image: "/tmp/shot.png"},
			want:   []string{"-p", "--permission-mode", "auto", prompt},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition, ok := registry.Lookup(test.agent)
			if !ok {
				t.Fatalf("no definition for %q", test.agent)
			}
			if got := definition.Render(test.mode, test.values); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("argv = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestRenderSkipsUnknownMode checks a mode a definition declares no template
// for renders nothing, so the caller reports it instead of invoking the agent
// with a bare prompt.
func TestRenderSkipsUnknownMode(t *testing.T) {
	definition := Definition{Args: Args{Run: []Fragment{{Args: []string{"go"}}}}}
	if got := definition.Render(ModeVision, Values{}); got != nil {
		t.Fatalf("vision argv = %#v, want nothing", got)
	}
	if definition.Supports(ModeVision) {
		t.Fatal("a definition with no vision template reported support for it")
	}
}

// TestInvalidDefinitionsNameTheirField checks every rejection says which field
// was wrong. A definition dropped without one is indistinguishable, at the
// --agent prompt, from a typo in the agent's name.
func TestInvalidDefinitionsNameTheirField(t *testing.T) {
	valid := func() Definition {
		return Definition{
			Name: "acme", Binary: "acme",
			Args:    Args{Run: []Fragment{{Args: []string{"{prompt}"}}}, Resume: []Fragment{{Args: []string{"{session}"}}}},
			Session: Session{Assign: AssignGlorp},
			Output:  Output{Format: FormatText},
		}
	}
	for _, test := range []struct {
		name    string
		mutate  func(*Definition)
		wantSub string
	}{
		{"no name", func(d *Definition) { d.Name = "" }, `"name"`},
		{"unusable name", func(d *Definition) { d.Name = "my agent" }, `"name"`},
		{"no binary", func(d *Definition) { d.Binary = "" }, `"binary"`},
		{"no run template", func(d *Definition) { d.Args.Run = nil }, `"args.run"`},
		{"no resume template", func(d *Definition) { d.Args.Resume = nil }, `"args.resume"`},
		{"empty fragment", func(d *Definition) { d.Args.Run = []Fragment{{}} }, `"args.run"[0].args`},
		{"unknown condition", func(d *Definition) {
			d.Args.Run = []Fragment{{When: "sandbox", Args: []string{"x"}}}
		}, `unknown condition "sandbox"`},
		{"unknown placeholder", func(d *Definition) {
			d.Args.Run = []Fragment{{Args: []string{"--say={greeting}"}}}
		}, `unknown placeholder {greeting}`},
		{"unknown assignment", func(d *Definition) { d.Session.Assign = "somehow" }, `"session.assign"`},
		{"capture without a pattern", func(d *Definition) { d.Session = Session{Assign: AssignCapture} }, `"session.capture"`},
		{"capture without a group", func(d *Definition) {
			d.Session = Session{Assign: AssignCapture, Capture: "session id"}
		}, "capture group"},
		{"capture that does not compile", func(d *Definition) {
			d.Session = Session{Assign: AssignCapture, Capture: "session id: ("}
		}, `"session.capture"`},
		{"capture where none is read", func(d *Definition) { d.Session.Capture = "x" }, `"session.capture"`},
		{"unknown output format", func(d *Definition) { d.Output.Format = "yaml" }, `"output.format"`},
		{"empty level", func(d *Definition) { d.Levels = []string{"high", " "} }, `"levels"`},
		{"empty model", func(d *Definition) { d.Models = []string{""} }, `"models"`},
		{"unusable env name", func(d *Definition) { d.Env = map[string]string{"A=B": "c"} }, `"env"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := valid()
			test.mutate(&definition)
			err := definition.Validate()
			if err == nil {
				t.Fatal("the definition was accepted")
			}
			if !strings.Contains(err.Error(), test.wantSub) {
				t.Fatalf("error = %v, want it to name %s", err, test.wantSub)
			}
		})
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("a valid definition was rejected: %v", err)
	}
}

// TestAllowListsAdmitAnythingWhenEmpty checks an allow-list left out of a
// definition constrains nothing, so a definition need not enumerate every
// model its agent has.
func TestAllowListsAdmitAnythingWhenEmpty(t *testing.T) {
	open := Definition{}
	if !open.AcceptsLevel("whatever") || !open.AcceptsModel("whatever") {
		t.Fatal("an empty allow-list rejected a value")
	}
	limited := Definition{Levels: []string{"low"}, Models: []string{"opus"}}
	if limited.AcceptsLevel("high") || limited.AcceptsModel("sonnet") {
		t.Fatal("an allow-list admitted a value it does not list")
	}
	if !limited.AcceptsLevel("low") || !limited.AcceptsModel("opus") {
		t.Fatal("an allow-list rejected a value it lists")
	}
}

func TestJoinOrReadsAsASentence(t *testing.T) {
	for _, test := range []struct {
		values []string
		want   string
	}{
		{nil, ""},
		{[]string{"low"}, "low"},
		{[]string{"low", "high"}, "low or high"},
		{[]string{"low", "medium", "high"}, "low, medium, or high"},
	} {
		if got := JoinOr(test.values); got != test.want {
			t.Fatalf("JoinOr(%v) = %q, want %q", test.values, got, test.want)
		}
	}
}

// TestSessionAccessors checks the two session shapes a definition can declare
// resolve to the behaviour the runner keys off.
func TestSessionAccessors(t *testing.T) {
	registry := MustBuiltin()
	claude, _ := registry.Lookup("claude")
	if !claude.AssignsSessionID() || claude.CapturesSessionID() || claude.SessionPattern() != nil {
		t.Fatal("claude should take the session ID glorp assigns it")
	}
	codex, _ := registry.Lookup("codex")
	if codex.AssignsSessionID() || !codex.CapturesSessionID() {
		t.Fatal("codex should announce its own session ID")
	}
	pattern := codex.SessionPattern()
	if pattern == nil {
		t.Fatal("codex declares no session pattern")
	}
	match := pattern.FindStringSubmatch("session id: 3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	if len(match) != 2 || match[1] != "3f2504e0-4f89-11d3-9a0c-0305e82c3301" {
		t.Fatalf("session pattern read %#v, want the announced ID", match)
	}
	if !codex.Session.ClearOnResumeFailure {
		t.Fatal("codex should drop a session ID it can no longer resume")
	}
}
