package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lsegal/glorp/agents"
)

// install.sh is documented to be run as `curl -fsSL ... | bash`, so bash reads
// the script itself from stdin. Any command in it that reads stdin consumes the
// rest of the script, which is how the final "Installed glorp ..." line went
// missing. Keep every npx call detached from the pipe.
func TestInstallShellScriptKeepsNpxOffStdin(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	var npxLines int
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "npx ") {
			continue
		}
		npxLines++
		if !strings.Contains(trimmed, "</dev/null") {
			t.Errorf("npx call must redirect stdin from /dev/null, got: %s", trimmed)
		}
	}
	if npxLines == 0 {
		t.Fatal("install.sh no longer contains any npx calls")
	}
	if !strings.Contains(string(data), `echo "Installed glorp $version`) {
		t.Error("install.sh must still report the installed version")
	}
}

// hardcodedAgentFlag matches a `--agent` whose value is a literal id rather
// than a value read from the registry, which is what the scripts must not do.
var hardcodedAgentFlag = regexp.MustCompile(`--agent\s+[A-Za-z][A-Za-z0-9-]*`)

// The installers install the gh-fix and gh-discuss skills for every agent in
// the registry by asking the binary they just installed for the list. Adding a
// built-in definition therefore has to be enough: a hardcoded "--agent codex"
// in either script is the bug these tests exist to catch.
func TestInstallersDeriveSkillAgentsFromTheRegistry(t *testing.T) {
	for _, script := range []string{"install.sh", "install.ps1"} {
		data, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("read %s: %v", script, err)
		}
		text := string(data)
		if !strings.Contains(text, "agents -skills") {
			t.Errorf("%s must read the agent list with `glorp agents -skills`", script)
		}
		for _, match := range hardcodedAgentFlag.FindAllString(text, -1) {
			t.Errorf("%s hardcodes an agent instead of using the registry: %s", script, match)
		}
		if !strings.Contains(text, "skills add") {
			t.Errorf("%s no longer installs the skills", script)
		}
	}
}

// Every built-in agent has to name the skills.sh target its skills install
// for, or the installer silently stops covering it.
func TestBuiltinAgentsNameASkillsTarget(t *testing.T) {
	registry := agents.MustBuiltin()
	names := registry.Names()
	if len(names) == 0 {
		t.Fatal("the registry has no built-in agents")
	}
	for _, name := range names {
		definition, _ := registry.Lookup(name)
		if definition.SkillsTarget() == "" {
			t.Errorf("built-in agent %q declares no \"skills\" target, so the installers would not install its skills", name)
		}
	}
	if got := registry.SkillsTargets(); len(got) == 0 {
		t.Fatal("SkillsTargets() is empty, so the installers would install nothing")
	}
}

// The command the installers call has to exist and print one target per line.
func TestAgentsSkillsCommandPrintsTheRegistryTargets(t *testing.T) {
	if _, ok := lookupCommand("agents"); !ok {
		t.Fatal("the `agents` command the installers call is missing")
	}
	stdout := captureStdout(t, func() {
		if code := runAgentsCommand([]string{"-skills", "-config", filepath.Join(t.TempDir(), agents.DefaultConfigPath)}); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	got := strings.Fields(stdout)
	if want := agents.MustBuiltin().SkillsTargets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

// A config-defined agent may declare its own target, and the list stays
// deduplicated when two agents share one.
func TestSkillsTargetsMergeConfigAndDeduplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), agents.DefaultConfigPath)
	document := `{"agents":[
		{"name":"acme","binary":"acme","skills":{"target":"universal"},
		 "session":{"assign":"none"},"output":{"format":"text"},
		 "args":{"run":[{"args":["run","{prompt}"]}],"resume":[{"args":["run","{prompt}"]}]}},
		{"name":"claude-next","binary":"claude","skills":{"target":"claude-code"},
		 "session":{"assign":"none"},"output":{"format":"text"},
		 "args":{"run":[{"args":["{prompt}"]}],"resume":[{"args":["{prompt}"]}]}}
	]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := agents.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// The expectation is the built-in targets plus the config's own, rather
	// than a written-out list, so an agent definition added later does not
	// have to edit a test about deduplication to say so.
	want := uniqueSorted(append(agents.MustBuiltin().SkillsTargets(), "universal"))
	got := registry.SkillsTargets()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	// claude-next names the same target as the built-in claude, which is the
	// duplicate this test exists to catch.
	seen := 0
	for _, target := range got {
		if target == "claude-code" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("targets = %v, want claude-code exactly once", got)
	}
}

// captureStdout collects what fn writes to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = saved }()
	fn()
	write.Close()
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// uniqueSorted is the shape SkillsTargets() returns: sorted with duplicates
// dropped. A test's expectation is built with it so naming a target a built-in
// already declares stays a statement about deduplication rather than a failure.
func uniqueSorted(values []string) []string {
	sort.Strings(values)
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if len(unique) == 0 || value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}
