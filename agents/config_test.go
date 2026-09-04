package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultConfigPath)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadWithoutAConfigFile checks a run with no config file has exactly the
// built-in agents. A missing file is the normal case, not an error.
func TestLoadWithoutAConfigFile(t *testing.T) {
	registry, err := Load(filepath.Join(t.TempDir(), DefaultConfigPath))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := registry.Names(), []string{"claude", "codex", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %v, want the built-ins %v", got, want)
	}
}

// TestConfigOverridesABuiltinFieldByField checks a definition that names a
// built-in replaces only the fields it mentions, so overriding one flag does
// not mean restating the whole agent.
func TestConfigOverridesABuiltinFieldByField(t *testing.T) {
	path := writeConfig(t, `{"agents":[{"name":"claude","binary":"/opt/claude","models":["opus"]}]}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	claude, ok := registry.Lookup("claude")
	if !ok {
		t.Fatal("claude was not registered")
	}
	if claude.Binary != "/opt/claude" {
		t.Fatalf("binary = %q, want the override", claude.Binary)
	}
	if !reflect.DeepEqual(claude.Models, []string{"opus"}) {
		t.Fatalf("models = %v, want the override", claude.Models)
	}
	// Everything the document did not mention is still the built-in's.
	if claude.Env["CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS"] != "0" {
		t.Fatalf("env = %v, want the built-in's", claude.Env)
	}
	if got := claude.Render(ModeRun, Values{Prompt: "go"}); !reflect.DeepEqual(got, []string{"-p", "--permission-mode", "auto", "--output-format", "stream-json", "--verbose", "go"}) {
		t.Fatalf("argv = %#v, want the built-in template", got)
	}
	if got, want := registry.Names(), []string{"claude", "codex", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
}

// TestConfigNullClearsAnInheritedField checks the documented way to remove
// something a built-in declares: give the field as null rather than trying to
// spell an empty value the schema has no syntax for.
func TestConfigNullClearsAnInheritedField(t *testing.T) {
	path := writeConfig(t, `{"agents":[{"name":"claude","env":null,"levels":null}]}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	claude, _ := registry.Lookup("claude")
	if len(claude.Env) != 0 {
		t.Fatalf("env = %v, want it cleared", claude.Env)
	}
	if len(claude.Levels) != 0 {
		t.Fatalf("levels = %v, want them cleared", claude.Levels)
	}
	if !claude.AcceptsLevel("blazing") {
		t.Fatal("a cleared allow-list should constrain nothing")
	}
}

// TestConfigRegistersANewAgent checks an unknown name adds an agent rather
// than being rejected for not being one of the built-ins.
func TestConfigRegistersANewAgent(t *testing.T) {
	path := writeConfig(t, `{"agents":[{
		"name":"muse","binary":"muse",
		"session":{"assign":"none"},"output":{"format":"text"},
		"args":{"run":[{"args":["run","{prompt}"]}],"resume":[{"args":["run","{prompt}"]}]}
	}]}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := registry.Names(), []string{"claude", "codex", "muse", "opencode"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %v, want %v", got, want)
	}
	muse, _ := registry.Lookup("muse")
	if got := muse.Render(ModeRun, Values{Prompt: "go"}); !reflect.DeepEqual(got, []string{"run", "go"}) {
		t.Fatalf("argv = %#v, want the configured template", got)
	}
}

// TestConfigAcceptsAnObjectKeyedByName checks the second shape the agents
// section may take, where the key names the agent instead of a "name" field.
func TestConfigAcceptsAnObjectKeyedByName(t *testing.T) {
	path := writeConfig(t, `{"agents":{"claude":{"binary":"/opt/claude"}}}`)
	registry, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	claude, _ := registry.Lookup("claude")
	if claude.Binary != "/opt/claude" {
		t.Fatalf("binary = %q, want the override", claude.Binary)
	}
}

// TestConfigErrorsNameTheFileTheAgentAndTheField checks every rejection is
// loud and specific. A definition dropped quietly looks exactly like a typo in
// --agent, which is the failure this whole file exists to avoid.
func TestConfigErrorsNameTheFileTheAgentAndTheField(t *testing.T) {
	for _, test := range []struct {
		name     string
		document string
		wantSubs []string
	}{
		{
			name:     "malformed json",
			document: `{"agents":[`,
			wantSubs: []string{DefaultConfigPath},
		},
		{
			name:     "unknown field",
			document: `{"agents":[{"name":"claude","binaries":"claude"}]}`,
			wantSubs: []string{DefaultConfigPath, `agent "claude"`, `unknown field "binaries"`},
		},
		{
			name:     "invalid value",
			document: `{"agents":[{"name":"claude","output":{"format":"yaml"}}]}`,
			wantSubs: []string{`agent "claude"`, `"output.format"`},
		},
		{
			name:     "unknown condition",
			document: `{"agents":[{"name":"claude","args":{"run":[{"when":"sandbox","args":["-x"]}],"resume":[{"args":["-x"]}]}}]}`,
			wantSubs: []string{`agent "claude"`, `unknown condition "sandbox"`},
		},
		{
			name:     "nameless definition",
			document: `{"agents":[{"binary":"muse"}]}`,
			wantSubs: []string{"agents[0]", `"name"`},
		},
		{
			name:     "key and name disagree",
			document: `{"agents":{"muse":{"name":"cline"}}}`,
			wantSubs: []string{`"muse"`, `"cline"`},
		},
		{
			name:     "unknown section",
			document: `{"targets":["o/r"]}`,
			wantSubs: []string{`unknown section "targets"`, `"agents"`},
		},
		{
			name:     "a work state file",
			document: `{"12":{"status":"completed"}}`,
			wantSubs: []string{"work-state record", "--state"},
		},
		{
			name:     "a scoped work state file",
			document: `{"o/r#12":{"status":"completed"}}`,
			wantSubs: []string{"work-state record", "--state"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.document))
			if err == nil {
				t.Fatal("the config was accepted")
			}
			for _, want := range test.wantSubs {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want it to mention %s", err, want)
				}
			}
		})
	}
}

// TestConfigIsNeverWritten checks loading leaves the file exactly as the user
// wrote it. The whole reason definitions do not live in the state file is that
// glorp rewrites that one.
func TestConfigIsNeverWritten(t *testing.T) {
	document := `{"agents":[{"name":"claude","binary":"/opt/claude"}]}`
	path := writeConfig(t, document)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != document {
		t.Fatalf("config file = %q, want it untouched", raw)
	}
}
