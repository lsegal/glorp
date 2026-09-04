package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lsegal/glorp/agents"
)

// TestConfigPathIsReadBeforeTheFlagsAre checks --config is found in the raw
// argument list whatever order it was written in. Definitions have to be
// loaded before --agent is validated against them, and the flag package hands
// values over in the order they appear, so `--agent mine --config x.json`
// would otherwise fail on an agent the config file defines.
func TestConfigPathIsReadBeforeTheFlagsAre(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"absent", []string{"--agent", "codex", "o/r"}, agents.DefaultConfigPath},
		{"after another flag", []string{"--agent", "mine", "--config", "custom.json"}, "custom.json"},
		{"single dash", []string{"-config", "custom.json"}, "custom.json"},
		{"joined value", []string{"--config=custom.json", "--agent", "mine"}, "custom.json"},
		{"after the terminator", []string{"--", "--config", "custom.json"}, agents.DefaultConfigPath},
		{"trailing with no value", []string{"--config"}, agents.DefaultConfigPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := configPathFromArgs(test.args, agents.DefaultConfigPath); got != test.want {
				t.Fatalf("configPathFromArgs(%v) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}

// TestWorkStateFileWithAgentDefinitionsIsRejected checks definitions written
// into the --state file are reported rather than destroyed. glorp rewrites
// that file as issues are handled, so a hand-edited definition living there
// would not survive the next save.
func TestWorkStateFileWithAgentDefinitionsIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".glorp.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"muse"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := guardWorkStateFile(path)
	if err == nil {
		t.Fatal("a state file holding agent definitions was accepted")
	}
	for _, want := range []string{path, agents.DefaultConfigPath, "--state"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %s", err, want)
		}
	}
	// A real work-state file, and a file that is not there at all, are both
	// none of this check's business.
	if err := os.WriteFile(path, []byte(`{"o/r#12":{"status":"completed"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guardWorkStateFile(path); err != nil {
		t.Fatalf("a work-state file was rejected: %v", err)
	}
	if err := guardWorkStateFile(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("a missing state file was rejected: %v", err)
	}
}

// TestUnknownAgentSpecListsTheKnownAgents checks the error --agent gives is
// built from the registry rather than from a hardcoded pair of names.
func TestUnknownAgentSpecListsTheKnownAgents(t *testing.T) {
	_, err := parseAgentSpec("gemini")
	if err == nil {
		t.Fatal("an unknown agent was accepted")
	}
	want := append([]string{`unknown agent "gemini"`}, agents.MustBuiltin().Names()...)
	for _, want := range want {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %s", err, want)
		}
	}
}

// TestAgentLevelErrorComesFromTheDefinition checks the level message is built
// from the agent's own allow-list, so an agent with different levels does not
// report Codex's.
func TestAgentLevelErrorComesFromTheDefinition(t *testing.T) {
	_, err := parseAgentSpec("codex:turbo")
	if err == nil || !strings.Contains(err.Error(), "agent level must be low, medium, or high") {
		t.Fatalf("error = %v, want the built-in levels", err)
	}
	registry, err := agents.NewRegistry(agents.Definition{
		Name: "muse", Binary: "muse", Levels: []string{"fast", "thorough"},
		Args:    agents.Args{Run: []agents.Fragment{{Args: []string{"{prompt}"}}}, Resume: []agents.Fragment{{Args: []string{"{prompt}"}}}},
		Session: agents.Session{Assign: agents.AssignNone},
		Output:  agents.Output{Format: agents.FormatText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAgentSpecIn(registry, "muse:high"); err == nil || !strings.Contains(err.Error(), "fast or thorough") {
		t.Fatalf("error = %v, want the definition's own levels", err)
	}
	if _, err := parseAgentSpecIn(registry, "muse:fast"); err != nil {
		t.Fatalf("a level the definition lists was rejected: %v", err)
	}
}

// TestAgentModelAllowListIsEnforced checks a definition that enumerates its
// models rejects one it does not list, and that the built-ins, which enumerate
// none, keep accepting any model.
func TestAgentModelAllowListIsEnforced(t *testing.T) {
	registry, err := agents.NewRegistry(agents.Definition{
		Name: "muse", Binary: "muse", Models: []string{"muse-1"},
		Args:    agents.Args{Run: []agents.Fragment{{Args: []string{"{prompt}"}}}, Resume: []agents.Fragment{{Args: []string{"{prompt}"}}}},
		Session: agents.Session{Assign: agents.AssignNone},
		Output:  agents.Output{Format: agents.FormatText},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAgentSpecIn(registry, "muse/muse-2"); err == nil || !strings.Contains(err.Error(), "muse-1") {
		t.Fatalf("error = %v, want the definition's own models", err)
	}
	if _, err := parseAgentSpec("claude/anything-at-all"); err != nil {
		t.Fatalf("an agent that lists no models rejected one: %v", err)
	}
}

// TestAgentAssignsSessionIDFollowsTheDefinition checks which agents glorp
// generates a session ID for is read from the registry, and that an agent it
// has never heard of keeps the historical behaviour of being given one.
func TestAgentAssignsSessionIDFollowsTheDefinition(t *testing.T) {
	if !agentAssignsSessionID("claude/opus:high") {
		t.Fatal("claude should be given a session ID")
	}
	if agentAssignsSessionID("codex") {
		t.Fatal("codex assigns its own session ID")
	}
	if !agentAssignsSessionID("some-old-agent") {
		t.Fatal("an unknown agent should keep the generated ID")
	}
}

// TestDefinitionEnvIsStable checks the child environment a definition asks for
// is rendered in a fixed order, so a run does not vary with map iteration.
func TestDefinitionEnvIsStable(t *testing.T) {
	definition := agents.Definition{Env: map[string]string{"B": "2", "A": "1", "C": "3"}}
	want := []string{"A=1", "B=2", "C=3"}
	for i := 0; i < 5; i++ {
		got := definitionEnv(definition)
		if len(got) != len(want) {
			t.Fatalf("env = %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("env = %v, want %v", got, want)
			}
		}
	}
	if got := definitionEnv(agents.Definition{}); got != nil {
		t.Fatalf("env = %v, want none", got)
	}
}

// TestBinaryFallsBackToTheDefinition checks an agent with no flag of its own
// is invoked through the executable its definition names, which is what makes
// a configured agent runnable at all.
func TestBinaryFallsBackToTheDefinition(t *testing.T) {
	registry, err := agents.NewRegistry(agents.Definition{
		Name: "muse", Binary: "/opt/muse",
		Args:    agents.Args{Run: []agents.Fragment{{Args: []string{"{prompt}"}}}, Resume: []agents.Fragment{{Args: []string{"{prompt}"}}}},
		Session: agents.Session{Assign: agents.AssignNone},
		Output:  agents.Output{Format: agents.FormatText},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := CommandRunner{Agent: "muse", Definitions: registry}
	if got := runner.binary("muse"); got != "/opt/muse" {
		t.Fatalf("binary = %q, want the definition's own", got)
	}
	// Binary holds the default agent's executable, so it does not stand in for
	// the configured agent's own.
	runner.Binary = "codex"
	if got := runner.binary("muse"); got != "/opt/muse" {
		t.Fatalf("binary = %q, want the definition's own rather than the default agent's", got)
	}
	// The agents that do have a flag of their own still take it.
	flagged := CommandRunner{Agent: "claude", ClaudeBinary: "claude-bin", CodexBinary: "codex-bin"}
	if got := flagged.binary("claude"); got != "claude-bin" {
		t.Fatalf("binary = %q, want the --claude-binary value", got)
	}
	if got := flagged.binary("codex"); got != "codex-bin" {
		t.Fatalf("binary = %q, want the --codex-binary value", got)
	}
}
