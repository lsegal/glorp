package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lsegal/glorp/agent"
)

// An agent definition cannot be verified in CI by invoking the real vendor
// CLI: none of them is installed on a runner and most need credentials. The
// harness below stands in for one instead. It reuses the stub runFakeAgent
// already re-executes the test binary as (see TestMain), so it runs unchanged
// on Windows and Unix, needs no network, and stays race-clean, and it drives a
// definition through the same CommandRunner a real dispatch goes through
// rather than through a parallel code path.
//
// Each agent added by issues #492-#495 gets a contract test on this harness,
// so it deliberately takes a definition it knows nothing about rather than
// hardcoding the two agents that exist today.

// agentContract is one agent definition wired to the fake CLI.
type agentContract struct {
	t          *testing.T
	spec       string
	definition *agent.Definition
	runner     CommandRunner
	log        string
}

// contractOptions configure what the fake CLI does when it is invoked.
type contractOptions struct {
	// RunOutput is printed by a fresh run: a session ID line, a
	// GLORP_CHECKOUT_DIRECTORY marker, stream-json events, anything the real
	// CLI would print. RunCode is the exit status that follows it.
	RunOutput string
	RunCode   int
	// ResumeOutput and ResumeCode are the same for a resume, and are how a
	// session the agent no longer holds is simulated.
	ResumeOutput string
	ResumeCode   int
	Yolo         bool
}

// clearEnvForTest removes a variable for the duration of one test and puts it
// back afterwards. The environment probe below has to read what glorp added to
// the child rather than what the test process was started with, and t.Setenv
// can only set a variable, never remove one.
func clearEnvForTest(t *testing.T, name string) {
	t.Helper()
	prior, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Setenv(name, prior); err != nil {
			t.Fatal(err)
		}
	})
}

// newAgentContract points an agent's definition at the fake CLI. spec is an
// --agent value, so a contract can pin a model and level as well as a name.
func newAgentContract(t *testing.T, spec string, options contractOptions) *agentContract {
	t.Helper()
	clearEnvForTest(t, "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS")
	parsed, err := parseAgentSpec(spec)
	if err != nil {
		t.Fatalf("parseAgentSpec(%q) error = %v", spec, err)
	}
	definition, ok := agentDefinition(parsed.Name)
	if !ok {
		t.Fatalf("agent %q has no definition", parsed.Name)
	}
	binary, log := writeFakeAgent(t, options.ResumeOutput, options.ResumeCode)
	t.Setenv(fakeAgentRunOutputEnv, options.RunOutput)
	t.Setenv(fakeAgentRunCodeEnv, strconv.Itoa(options.RunCode))
	return &agentContract{
		t: t, spec: spec, definition: definition, log: log,
		runner: CommandRunner{Binary: binary, Agent: spec, Repo: "o/r", Yolo: options.Yolo},
	}
}

// dispatch runs the issue through CommandRunner exactly as a watch does and
// returns everything the agent session picked up along the way.
func (c *agentContract) dispatch(session AgentSession) (AgentSession, string, error) {
	c.t.Helper()
	var mu sync.Mutex
	var output strings.Builder
	result := session
	err := c.runner.RunSessionWithOutput(context.Background(), Issue{Number: 7, Target: "o/r"}, session, func(update AgentSession) {
		mu.Lock()
		defer mu.Unlock()
		if update.ID != "" {
			result.ID = update.ID
		}
		if update.CheckoutDirectory != "" {
			result.CheckoutDirectory = update.CheckoutDirectory
		}
	}, &output)
	mu.Lock()
	defer mu.Unlock()
	return result, output.String(), err
}

// invocations returns one record per time the fake CLI was run, each holding
// the working directory, whether the definition's environment arrived, and the
// argv joined by spaces.
func (c *agentContract) invocations() []string {
	c.t.Helper()
	return fakeAgentInvocations(c.t, c.log)
}

// wantArgs is the argv the definition renders for a mode, joined the way the
// fake CLI logs it, so a mismatch reports the whole invocation.
func (c *agentContract) wantArgs(session AgentSession) string {
	c.t.Helper()
	return strings.Join(commandArgsForSession(c.runner, Issue{Number: 7, Target: "o/r"}, session), " ")
}

func (c *agentContract) assertArgs(record, want string) {
	c.t.Helper()
	args := record
	if index := strings.Index(record, "\n"); index >= 0 {
		// The first two lines are the working directory and the environment
		// probe; the argv follow.
		parts := strings.SplitN(record, "\n", 3)
		if len(parts) == 3 {
			args = parts[2]
		}
	}
	if args != want {
		c.t.Fatalf("agent %q was invoked with\n%q\nwant\n%q", c.spec, args, want)
	}
}

// TestAgentContractCodexCapturesItsOwnSession drives the codex definition end
// to end: the argv reach the CLI, the session ID it prints is picked up, and
// the checkout directory marker is followed.
func TestAgentContractCodexCapturesItsOwnSession(t *testing.T) {
	checkout := t.TempDir()
	sessionID := "0199a213-81c0-7800-8aa1-bbab2a035a53"
	contract := newAgentContract(t, "codex/gpt-5.6:high", contractOptions{
		RunOutput: "OpenAI Codex\nsession id: " + sessionID + "\nGLORP_CHECKOUT_DIRECTORY=" + checkout,
	})
	session, output, err := contract.dispatch(AgentSession{Agent: "codex/gpt-5.6:high"})
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	records := contract.invocations()
	if len(records) != 1 {
		t.Fatalf("invocations = %#v, want one", records)
	}
	contract.assertArgs(records[0], contract.wantArgs(AgentSession{Agent: "codex/gpt-5.6:high"}))
	if got, want := session.ID, sessionID; got != want {
		t.Fatalf("captured session ID = %q, want %q", got, want)
	}
	if got, want := session.CheckoutDirectory, checkout; got != want {
		t.Fatalf("captured checkout = %q, want %q", got, want)
	}
	// Codex output is passed through rather than decoded.
	if !strings.Contains(output, "session id: "+sessionID) {
		t.Fatalf("agent output was not forwarded verbatim: %q", output)
	}
	// The definition names no environment, so glorp adds nothing to the
	// child's beyond what it inherits.
	if strings.Contains(records[0], "bg_wait_ceiling_set=true") {
		t.Fatalf("codex run carried a variable its definition does not declare:\n%s", records[0])
	}
}

// TestAgentContractClaudeTakesAnAssignedSession is the other half of the
// session contract: glorp hands the ID over on the command line and decodes
// the agent's stream-json output rather than reading an ID back.
func TestAgentContractClaudeTakesAnAssignedSession(t *testing.T) {
	contract := newAgentContract(t, "claude", contractOptions{
		RunOutput: `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}`,
	})
	session := AgentSession{ID: "session-12", Agent: "claude"}
	got, output, err := contract.dispatch(session)
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	records := contract.invocations()
	if len(records) != 1 {
		t.Fatalf("invocations = %#v, want one", records)
	}
	contract.assertArgs(records[0], contract.wantArgs(session))
	if !strings.Contains(records[0], "--session-id session-12") {
		t.Fatalf("assigned session ID did not reach the agent:\n%s", records[0])
	}
	if got.ID != session.ID {
		t.Fatalf("session ID changed to %q; claude reports none to capture", got.ID)
	}
	// The definition asks for the stream-json decoder, so the dashboard sees
	// the text rather than the raw event.
	if strings.TrimSpace(output) != "working on it" {
		t.Fatalf("decoded output = %q, want the assistant text alone", output)
	}
	// And the definition's environment reaches the child process.
	if !strings.Contains(records[0], "bg_wait_ceiling_set=true bg_wait_ceiling=0") {
		t.Fatalf("claude run did not carry its definition's environment:\n%s", records[0])
	}
}

// TestAgentContractRestartsWhenAResumeFindsNoSession covers the third mode: a
// resume the agent cannot honour restarts the work as a fresh run, and whether
// the recorded ID survives that restart is the definition's decision.
func TestAgentContractRestartsWhenAResumeFindsNoSession(t *testing.T) {
	for _, test := range []struct {
		spec      string
		sessionID string
		// wantSecondID is the session ID the restart is expected to run with:
		// codex assigns its own, so the dead one is dropped, while claude's was
		// glorp's to give and is reused.
		wantSecondID string
	}{
		{spec: "codex", sessionID: "session-7", wantSecondID: ""},
		{spec: "claude", sessionID: "session-7", wantSecondID: "session-7"},
	} {
		t.Run(test.spec, func(t *testing.T) {
			contract := newAgentContract(t, test.spec, contractOptions{
				ResumeOutput: "Error: session not found",
				ResumeCode:   1,
			})
			session := AgentSession{ID: test.sessionID, Agent: test.spec, Resume: true}
			if _, _, err := contract.dispatch(session); err != nil {
				t.Fatalf("a missing session should restart the work, got %v", err)
			}
			records := contract.invocations()
			if len(records) != 2 {
				t.Fatalf("invocations = %#v, want a resume followed by a restart", records)
			}
			contract.assertArgs(records[0], contract.wantArgs(session))
			restarted := AgentSession{ID: test.wantSecondID, Agent: test.spec}
			contract.assertArgs(records[1], contract.wantArgs(restarted))
		})
	}
}

// TestAgentContractReportsAFailingAgent checks a non-zero exit that is not a
// missing session is still reported, rather than being restarted forever.
func TestAgentContractReportsAFailingAgent(t *testing.T) {
	contract := newAgentContract(t, "codex", contractOptions{RunOutput: "boom: the agent crashed", RunCode: 3})
	_, _, err := contract.dispatch(AgentSession{Agent: "codex"})
	if err == nil {
		t.Fatal("a failing agent reported success")
	}
	if !strings.Contains(err.Error(), "boom: the agent crashed") {
		t.Fatalf("error = %v, want it to quote what the agent printed", err)
	}
	if got := contract.invocations(); len(got) != 1 {
		t.Fatalf("invocations = %#v, want one", got)
	}
}

// TestAgentContractRunsAnAgentDefinedOnlyByConfig is the acceptance criterion
// the harness exists for: a CLI glorp ships no code for at all is declared in
// a config file, accepted by --agent, and dispatched.
func TestAgentContractRunsAnAgentDefinedOnlyByConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), agent.DefaultConfigPath)
	// The definition names the executable, since a config-defined agent has no
	// --<agent>-binary flag of its own yet (issue #489). Pointing it at the
	// fake CLI is therefore also what proves the "binary" field is honoured.
	fake, err := json.Marshal(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	definition := `{
	  "agents": [
	    {
	      "name": "muse",
	      "binary": ` + string(fake) + `,
	      "levels": ["low", "high"],
	      "env": {"MUSE_HEADLESS": "1"},
	      "args": {
	        "run": [
	          {"args": ["work"]},
	          {"args": ["--unsafe"], "when": "yolo"},
	          {"args": ["--model", "${model}"], "when": "model"},
	          {"args": ["--effort", "${level}"], "when": "level"},
	          {"args": ["${prompt}"]}
	        ],
	        "resume": [{"args": ["work", "--continue", "${session}", "${prompt}"]}]
	      },
	      "session": {"capturePattern": "muse session ([a-z0-9-]+)", "clearOnResumeFailure": true}
	    }
	  ]
	}`
	if err := os.WriteFile(path, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := agentRegistry()
	t.Cleanup(func() { setAgentRegistry(restore) })
	registry, err := agent.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	setAgentRegistry(registry)

	// --agent accepts it, with its own allow-list applied.
	if _, err := parseAgentSpec("muse/muse-1:high"); err != nil {
		t.Fatalf("--agent rejected the configured agent: %v", err)
	}
	if _, err := parseAgentSpec("muse:turbo"); err == nil {
		t.Fatal("--agent accepted a level the definition does not allow")
	}
	if _, err := parseAgentSpec("cline"); err == nil {
		t.Fatal("--agent accepted an agent nothing defines")
	}

	contract := newAgentContract(t, "muse/muse-1:high", contractOptions{
		RunOutput: "muse session abc-123",
		Yolo:      true,
	})
	// Nothing points the runner at the fake; the definition does.
	contract.runner.Binary = ""
	session, _, err := contract.dispatch(AgentSession{Agent: "muse/muse-1:high"})
	if err != nil {
		t.Fatalf("dispatch error = %v", err)
	}
	records := contract.invocations()
	if len(records) != 1 {
		t.Fatalf("invocations = %#v, want one", records)
	}
	contract.assertArgs(records[0], contract.wantArgs(AgentSession{Agent: "muse/muse-1:high"}))
	if !strings.Contains(records[0], "work --unsafe --model muse-1 --effort high") {
		t.Fatalf("configured argv did not reach the agent:\n%s", records[0])
	}
	if got, want := session.ID, "abc-123"; got != want {
		t.Fatalf("captured session ID = %q, want %q", got, want)
	}
	if got, want := (CommandRunner{Binary: "codex"}).binary("muse"), os.Args[0]; got != want {
		t.Fatalf("binary for muse = %q, want %q", got, want)
	}
	// Its quota is reported as untracked rather than read with another
	// agent's reader.
	readers := namedQuotaReaders([]string{"muse"}, func(string) string { return "muse-cli" })
	if len(readers) != 1 || readers[0].read(context.Background()) != "" {
		t.Fatalf("quota readers = %#v, want one untracked reader", readers)
	}
}

// TestConfigPathIsReadBeforeTheFlagsAreParsed pins the ordering --agent
// validation depends on: the flag package reads flags in the order they were
// typed, so --config has to be found ahead of parsing.
func TestConfigPathIsReadBeforeTheFlagsAreParsed(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--agent", "muse", "--config", "custom.json"}, "custom.json"},
		{[]string{"-config=custom.json", "o/r"}, "custom.json"},
		{[]string{"--config=custom.json"}, "custom.json"},
		{[]string{"-config", "custom.json"}, "custom.json"},
		{[]string{"--agent", "codex"}, ""},
		{[]string{"--", "--config", "custom.json"}, ""},
	} {
		if got, _ := configPathFromArgs(test.args); got != test.want {
			t.Fatalf("configPathFromArgs(%#v) = %q, want %q", test.args, got, test.want)
		}
	}
}

// TestWorkStateFileRejectsAgentDefinitions is the mirror of the config file's
// own guard: the two files are easy to mix up, so each says which is which.
func TestWorkStateFileRejectsAgentDefinitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".glorp.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"muse"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, load := range []func() error{
		func() error { _, err := loadWorkState(path); return err },
		func() error { _, err := loadScopedWorkState(path, []string{"o/r"}); return err },
	} {
		err := load()
		if err == nil {
			t.Fatal("agent definitions were accepted as work state")
		}
		if !strings.Contains(err.Error(), "--config") {
			t.Fatalf("error = %v, want it to point at the config file", err)
		}
	}
}

// TestUnknownAgentErrorListsTheRegistry replaces the old "agent must be codex
// or claude" message, which named agents rather than the registry and so went
// stale the moment a definition was added.
func TestUnknownAgentErrorListsTheRegistry(t *testing.T) {
	_, err := parseAgentSpec("gemini")
	if err == nil {
		t.Fatal("an undefined agent was accepted")
	}
	for _, want := range []string{`unknown agent "gemini"`, "claude", "codex"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %q", err, want)
		}
	}
}

// TestAgentDefinitionFallsBackToTheDefault covers work state written before a
// definition was removed, which must not leave a run with no argv at all.
func TestAgentDefinitionFallsBackToTheDefault(t *testing.T) {
	definition := agentDefinitionOrDefault("an-agent-that-was-removed")
	if definition == nil || definition.Name != defaultAgentName {
		t.Fatalf("fallback definition = %#v, want %q", definition, defaultAgentName)
	}
	if got := commandArgs(CommandRunner{Agent: "an-agent-that-was-removed"}, Issue{Number: 7}); !reflect.DeepEqual(got[:1], []string{"exec"}) {
		t.Fatalf("args for an unknown agent = %#v, want the default agent's", got)
	}
}
