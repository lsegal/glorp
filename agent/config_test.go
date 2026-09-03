package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultConfigPath)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadWithoutConfigUsesBuiltins covers the ordinary case: no config file
// exists and glorp runs on the definitions it ships with.
func TestLoadWithoutConfigUsesBuiltins(t *testing.T) {
	registry, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing config was an error: %v", err)
	}
	if got, want := registry.Names(), []string{"claude", "codex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %#v, want %#v", got, want)
	}
}

// TestConfigAddsAgent registers a CLI glorp has never heard of, which is the
// point of the whole schema: a new agent is a JSON blob, not a code change.
func TestConfigAddsAgent(t *testing.T) {
	path := writeConfig(t, `{
	  "agents": [
	    {
	      "name": "muse",
	      "binary": "muse-cli",
	      "levels": ["low", "high"],
	      "args": {
	        "run": [
	          {"args": ["run", "--id", "${session}"]},
	          {"args": ["--unsafe"], "when": "yolo"},
	          {"args": ["--model", "${model}"], "when": "model"},
	          {"args": ["${prompt}"]}
	        ],
	        "resume": [{"args": ["run", "--continue", "${session}", "${prompt}"]}]
	      },
	      "session": {"assign": true}
	    }
	  ]
	}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := registry.Names(), []string{"claude", "codex", "muse"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %#v, want %#v", got, want)
	}
	muse, ok := registry.Lookup("muse")
	if !ok {
		t.Fatal("muse was not registered")
	}
	got := muse.RenderArgs(ModeRun, Values{Prompt: "do it", Session: "s-1", Model: "muse-1", Yolo: true})
	want := []string{"run", "--id", "s-1", "--unsafe", "--model", "muse-1", "do it"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("muse run args = %#v, want %#v", got, want)
	}
	if !muse.AllowsLevel("high") || muse.AllowsLevel("turbo") {
		t.Fatal("muse did not apply its own level allow-list")
	}
	// The built-ins are untouched by a file that only adds an agent.
	codex, _ := registry.Lookup("codex")
	if got, want := codex.Binary, "codex"; got != want {
		t.Fatalf("codex binary = %q, want %q", got, want)
	}
}

// TestConfigOverridesBuiltinFieldByField checks the merge keeps every field the
// override leaves out, so changing one thing does not mean restating the argv.
func TestConfigOverridesBuiltinFieldByField(t *testing.T) {
	path := writeConfig(t, `{"agents": {"claude": {"binary": "claude-next", "levels": ["high"]}}}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	claude, _ := registry.Lookup("claude")
	if got, want := claude.Binary, "claude-next"; got != want {
		t.Fatalf("binary = %q, want %q", got, want)
	}
	if got, want := claude.Levels, []string{"high"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("levels = %#v, want %#v", got, want)
	}
	// Everything the override said nothing about is still the built-in value.
	want := []string{"-p", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", "do it"}
	if got := claude.RenderArgs(ModeRun, Values{Prompt: "do it"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("run args = %#v, want %#v", got, want)
	}
	if got, want := claude.EnvPairs(), []string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}
	if got, want := claude.Output.Format, OutputClaudeStreamJSON; got != want {
		t.Fatalf("output format = %q, want %q", got, want)
	}
}

// TestConfigNullsOutInheritedFields covers the documented way to remove
// something a built-in contributed, rather than leaving it unremovable.
func TestConfigNullsOutInheritedFields(t *testing.T) {
	path := writeConfig(t, `{"agents": {"claude": {"env": null, "levels": [], "args": {"vision": []}}}}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	claude, _ := registry.Lookup("claude")
	if got := claude.EnvPairs(); got != nil {
		t.Fatalf("env = %#v, want none", got)
	}
	if !claude.AllowsLevel("turbo") {
		t.Fatal("an emptied level allow-list still rejected a level")
	}
	if got := claude.RenderArgs(ModeVision, Values{Prompt: "read it"}); got != nil {
		t.Fatalf("vision args = %#v, want none", got)
	}
	// Emptying vision leaves the modes the override did not mention alone.
	if got := claude.RenderArgs(ModeRun, Values{Prompt: "do it"}); len(got) == 0 {
		t.Fatal("run args were dropped along with vision")
	}
}

func TestConfigRejectsBadInput(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"malformed json", `{"agents": [`, "decode agent config"},
		{"unknown top level section", `{"agent": []}`, "decode agent config"},
		{"unknown field", `{"agents":[{"name":"muse","binry":"muse"}]}`, "binry"},
		{"name disagrees with its key", `{"agents":{"muse":{"name":"cline","binary":"c","args":{"run":[{"args":["${prompt}"]}]},"session":{"assign":true}}}}`, `names itself "cline"`},
		{"agents is neither shape", `{"agents": 3}`, "array of agent definitions or an object keyed by agent name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.body))
			if err == nil {
				t.Fatal("bad config was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

// TestConfigRecognisesAWorkStateFile checks the two files that are easy to mix
// up say so. A work-state file handed to --config otherwise fails as an
// unknown field, which explains nothing about which file it belongs in.
func TestConfigRecognisesAWorkStateFile(t *testing.T) {
	for _, body := range []string{
		`{"7": {"status": "completed"}, "12": {"status": "active", "sessionId": "s-1"}}`,
		`{"owner/repo#7": {"status": "completed"}}`,
	} {
		_, err := Load(writeConfig(t, body))
		if err == nil {
			t.Fatalf("work state %s was accepted as agent config", body)
		}
		if !strings.Contains(err.Error(), "--state") {
			t.Fatalf("error = %v, want it to point at the state file", err)
		}
	}
}

func TestEmptyConfigIsNotAnError(t *testing.T) {
	registry, err := Load(writeConfig(t, "  \n"))
	if err != nil {
		t.Fatalf("empty config was an error: %v", err)
	}
	if got, want := len(registry.Names()), 2; got != want {
		t.Fatalf("agents = %d, want %d", got, want)
	}
	if _, err := Load(writeConfig(t, `{"agents": []}`)); err != nil {
		t.Fatalf("config with no agents was an error: %v", err)
	}
}

func TestUnknownAgentErrorListsTheKnownOnes(t *testing.T) {
	registry, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	err = registry.UnknownAgentError("gemini")
	for _, want := range []string{`"gemini"`, "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %q", err, want)
		}
	}
}

// TestCloneLeavesTheOriginalAlone matters because the built-ins are shared:
// merging a config over a clone must not change what another reader sees.
func TestCloneLeavesTheOriginalAlone(t *testing.T) {
	registry, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	clone := registry.Clone()
	if err := clone.Apply("agents.json", []byte(`{"agents":{"claude":{"binary":"claude-next","env":{"EXTRA":"1"}}}}`)); err != nil {
		t.Fatal(err)
	}
	original, _ := registry.Lookup("claude")
	if got, want := original.Binary, "claude"; got != want {
		t.Fatalf("original binary = %q, want %q", got, want)
	}
	if got, want := original.EnvPairs(), []string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("original env = %#v, want %#v", got, want)
	}
	// A partial env object adds to what the built-in set rather than replacing
	// it, which is the one field where merging is per key.
	merged, _ := clone.Lookup("claude")
	if got, want := merged.EnvPairs(), []string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=0", "EXTRA=1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged env = %#v, want %#v", got, want)
	}
}
