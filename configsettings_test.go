package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/agents"
	"github.com/lsegal/glorp/core"
)

// writeConfig drops a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), agents.DefaultConfigPath)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// parseWatchWithConfig parses args the way runWatch does: the command line
// first, then the config file's settings section for whatever it left alone.
func parseWatchWithConfig(t *testing.T, path string, args ...string) (*flagSetUnderTest, error) {
	t.Helper()
	specs := agentFlag{values: []agentSpec{{Name: "codex"}}}
	binaries := agentBinaryFlag{}
	filter := filterFlag{values: []string{defaultIssueFilter}}
	flags := watchFlagSet(&specs, &binaries, &filter)
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	settings, err := loadConfigSettings(path)
	if err != nil {
		return nil, err
	}
	if err := applyConfigSettings(flags, path, settings); err != nil {
		return nil, err
	}
	return &flagSetUnderTest{flags: flags, specs: &specs, binaries: &binaries, filter: &filter}, nil
}

type flagSetUnderTest struct {
	flags    *flag.FlagSet
	specs    *agentFlag
	binaries *agentBinaryFlag
	filter   *filterFlag
}

// TestConfigSettingsSupplyWatchDefaults checks that a settings section fills
// in switches of every kind the command line did not give (issue #614).
func TestConfigSettingsSupplyWatchDefaults(t *testing.T) {
	path := writeConfig(t, `{"settings": {
		"concurrency": 7,
		"pollmode": "poll",
		"no-headless": true,
		"interval": "90s",
		"ready-state": "Queued",
		"agent": ["claude/opus", "codex"],
		"filter": ["is:open label:bug", "is:open label:chore"]
	}}`)
	parsed, err := parseWatchWithConfig(t, path)
	if err != nil {
		t.Fatalf("apply config settings: %v", err)
	}
	if got := flagValue[int](parsed.flags, "concurrency"); got != 7 {
		t.Errorf("concurrency = %d, want 7", got)
	}
	if got := flagValue[string](parsed.flags, "pollmode"); got != "poll" {
		t.Errorf("pollmode = %q, want %q", got, "poll")
	}
	if !flagValue[bool](parsed.flags, "no-headless") {
		t.Error("no-headless = false, want true")
	}
	if got := flagValue[time.Duration](parsed.flags, "interval"); got != 90*time.Second {
		t.Errorf("interval = %s, want 90s", got)
	}
	if got := flagValue[string](parsed.flags, "ready-state"); got != "Queued" {
		t.Errorf("ready-state = %q, want %q", got, "Queued")
	}
	if got := strings.Join(parsed.specs.specs(), ","); got != "claude/opus,codex" {
		t.Errorf("agents = %q, want %q", got, "claude/opus,codex")
	}
	if got := len(parsed.filter.values); got != 2 {
		t.Errorf("filters = %d, want 2", got)
	}
}

// TestCommandLineBeatsConfigSettings checks the file supplies defaults rather
// than overrides: a switch written on the command line keeps its value.
func TestCommandLineBeatsConfigSettings(t *testing.T) {
	path := writeConfig(t, `{"settings": {"concurrency": 7, "agent": ["claude"], "pollmode": "poll"}}`)
	parsed, err := parseWatchWithConfig(t, path, "-concurrency", "2", "-agent", "codex")
	if err != nil {
		t.Fatalf("apply config settings: %v", err)
	}
	if got := flagValue[int](parsed.flags, "concurrency"); got != 2 {
		t.Errorf("concurrency = %d, want 2 from the command line", got)
	}
	if got := strings.Join(parsed.specs.specs(), ","); got != "codex" {
		t.Errorf("agents = %q, want %q from the command line", got, "codex")
	}
	if got := flagValue[string](parsed.flags, "pollmode"); got != "poll" {
		t.Errorf("pollmode = %q, want %q from the file", got, "poll")
	}
}

// TestConfigSettingsRejectBadKeysAndValues checks a mistake in the file stops
// the run naming the file and the key, the way a bad agent definition does,
// instead of being dropped where it is indistinguishable from a typo.
func TestConfigSettingsRejectBadKeysAndValues(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"unknown switch", `{"settings": {"concurrencey": 3}}`, `unknown switch "concurrencey"`},
		{"config is circular", `{"settings": {"config": "other.json"}}`, "it selects this file"},
		{"unusable value", `{"settings": {"concurrency": {"n": 3}}}`, "must be a string, number, boolean"},
		{"invalid value", `{"settings": {"interval": "soon"}}`, `"interval"`},
		{"section not an object", `{"settings": ["concurrency"]}`, "must be an object keyed by switch name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, test.body)
			_, err := parseWatchWithConfig(t, path)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error = %q, want it to mention %q", err, test.want)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %q, want it to name %q", err, path)
			}
		})
	}
}

// TestConfigSettingsAcceptDashedKeys checks a switch written the way it is
// typed on the command line is the same switch.
func TestConfigSettingsAcceptDashedKeys(t *testing.T) {
	path := writeConfig(t, `{"settings": {"--concurrency": 5}}`)
	parsed, err := parseWatchWithConfig(t, path)
	if err != nil {
		t.Fatalf("apply config settings: %v", err)
	}
	if got := flagValue[int](parsed.flags, "concurrency"); got != 5 {
		t.Errorf("concurrency = %d, want 5", got)
	}
}

// TestMissingConfigSettingsAreNotAnError checks that no file, no section, and
// an empty path all leave the defaults alone, matching agents.Load.
func TestMissingConfigSettingsAreNotAnError(t *testing.T) {
	for _, test := range []struct{ name, path string }{
		{"empty path", ""},
		{"missing file", filepath.Join(t.TempDir(), "absent.json")},
		{"no section", writeConfig(t, `{"agents": []}`)},
		{"null section", writeConfig(t, `{"settings": null}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings, err := loadConfigSettings(test.path)
			if err != nil {
				t.Fatalf("load config settings: %v", err)
			}
			if len(settings) != 0 {
				t.Errorf("settings = %v, want none", settings)
			}
		})
	}
}

// TestAgentLoaderIgnoresSettingsSection checks the agent loader tolerates the
// new section beside its own instead of rejecting the file as unknown.
func TestAgentLoaderIgnoresSettingsSection(t *testing.T) {
	path := writeConfig(t, `{"settings": {"concurrency": 4}, "agents": []}`)
	if _, err := agents.Load(path); err != nil {
		t.Fatalf("load agents alongside a settings section: %v", err)
	}
	path = writeConfig(t, `{"nonsense": {}}`)
	if _, err := agents.Load(path); err == nil {
		t.Fatal("expected an unknown section to still be rejected")
	}
}

// TestSaveConfigSettingsMergesIntoFile checks the dashboard's write-back
// keeps every other section and every switch it does not mention.
func TestSaveConfigSettingsMergesIntoFile(t *testing.T) {
	path := writeConfig(t, `{"agents": [{"name": "robo", "binary": "robo", "args": {"run": []}}], "settings": {"pollmode": "poll", "concurrency": 1}}`)
	if err := saveConfigSettings(path, map[string]any{"concurrency": 9, "agent": []string{"claude", "codex"}}); err != nil {
		t.Fatalf("save config settings: %v", err)
	}
	var top map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("reread config: %v", err)
	}
	if _, ok := top["agents"]; !ok {
		t.Error("agents section was dropped")
	}
	var settings map[string]any
	if err := json.Unmarshal(top["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["pollmode"] != "poll" {
		t.Errorf("pollmode = %v, want it left alone", settings["pollmode"])
	}
	if settings["concurrency"] != float64(9) {
		t.Errorf("concurrency = %v, want 9", settings["concurrency"])
	}
	if got, _ := json.Marshal(settings["agent"]); string(got) != `["claude","codex"]` {
		t.Errorf("agent = %s, want [\"claude\",\"codex\"]", got)
	}
	// What was written must read back as defaults on the next start.
	parsed, err := parseWatchWithConfig(t, path)
	if err != nil {
		t.Fatalf("reapply saved settings: %v", err)
	}
	if got := flagValue[int](parsed.flags, "concurrency"); got != 9 {
		t.Errorf("reloaded concurrency = %d, want 9", got)
	}
	if got := strings.Join(parsed.specs.specs(), ","); got != "claude,codex" {
		t.Errorf("reloaded agents = %q, want %q", got, "claude,codex")
	}
}

// TestSaveConfigSettingsCreatesFile checks a run with no config file yet gets
// one when the dashboard saves a setting.
func TestSaveConfigSettingsCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), agents.DefaultConfigPath)
	if err := saveConfigSettings(path, map[string]any{"concurrency": 3}); err != nil {
		t.Fatalf("save config settings: %v", err)
	}
	settings, err := loadConfigSettings(path)
	if err != nil {
		t.Fatalf("load config settings: %v", err)
	}
	if string(settings["concurrency"]) != "3" {
		t.Errorf("concurrency = %s, want 3", settings["concurrency"])
	}
}

// TestSettingsUpdateFlagsMapsDashboardFields checks each live-editable
// setting is stored under the switch that configures the same thing.
func TestSettingsUpdateFlagsMapsDashboardFields(t *testing.T) {
	concurrency, readyState := 4, "Ready"
	commenters := []string{"alice", "bob"}
	active := []string{"claude/opus"}
	values := settingsUpdateFlags(core.SettingsUpdate{
		Concurrency:       &concurrency,
		ReadyState:        &readyState,
		AllowedCommenters: &commenters,
		ActiveAgents:      &active,
	})
	if values["concurrency"] != 4 {
		t.Errorf("concurrency = %v, want 4", values["concurrency"])
	}
	if values["ready-state"] != "Ready" {
		t.Errorf("ready-state = %v, want Ready", values["ready-state"])
	}
	if values["allowed-commenters"] != "alice,bob" {
		t.Errorf("allowed-commenters = %v, want alice,bob", values["allowed-commenters"])
	}
	if got, _ := json.Marshal(values["agent"]); string(got) != `["claude/opus"]` {
		t.Errorf("agent = %s, want [\"claude/opus\"]", got)
	}
	if len(settingsUpdateFlags(core.SettingsUpdate{})) != 0 {
		t.Error("an empty update should write nothing")
	}
}

// TestPersistingSettingsHandlerWritesAcceptedChanges checks the dashboard's
// handler saves what the running instance accepted, and only that.
func TestPersistingSettingsHandlerWritesAcceptedChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), agents.DefaultConfigPath)
	concurrency := 6
	handler := persistingSettingsHandler(func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error) {
		return core.SettingsSnapshot{Concurrency: concurrency}, nil
	}, path, nil)
	if _, err := handler(context.Background(), core.SettingsUpdate{Concurrency: &concurrency}); err != nil {
		t.Fatalf("apply settings: %v", err)
	}
	settings, err := loadConfigSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(settings["concurrency"]) != "6" {
		t.Errorf("saved concurrency = %s, want 6", settings["concurrency"])
	}
	// A rejected update must not be written, and a read of the current
	// settings must not rewrite the file either.
	rejected := persistingSettingsHandler(func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error) {
		return core.SettingsSnapshot{}, errors.New("nope")
	}, filepath.Join(t.TempDir(), agents.DefaultConfigPath), nil)
	if _, err := rejected(context.Background(), core.SettingsUpdate{Concurrency: &concurrency}); err == nil {
		t.Fatal("expected the rejected update to be reported")
	}
	empty := filepath.Join(t.TempDir(), agents.DefaultConfigPath)
	read := persistingSettingsHandler(func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error) {
		return core.SettingsSnapshot{}, nil
	}, empty, nil)
	if _, err := read(context.Background(), core.SettingsUpdate{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Error("reading the settings should not create the config file")
	}
}

// TestPersistingSettingsHandlerReportsWriteFailures checks an unwritable file
// is logged rather than failing an update that has already taken effect.
func TestPersistingSettingsHandlerReportsWriteFailures(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be makes every write fail.
	path := filepath.Join(dir, agents.DefaultConfigPath)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var logged []string
	concurrency := 2
	handler := persistingSettingsHandler(func(context.Context, core.SettingsUpdate) (core.SettingsSnapshot, error) {
		return core.SettingsSnapshot{Concurrency: concurrency}, nil
	}, path, func(format string, args ...interface{}) {
		logged = append(logged, format)
	})
	if _, err := handler(context.Background(), core.SettingsUpdate{Concurrency: &concurrency}); err != nil {
		t.Fatalf("a failed save must not fail the update: %v", err)
	}
	if len(logged) != 1 {
		t.Errorf("logged %d lines, want 1", len(logged))
	}
}
