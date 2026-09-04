package main

import (
	"github.com/lsegal/glorp/agents"
	"github.com/lsegal/glorp/core"
	"github.com/lsegal/glorp/webui"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConcurrencySemaphoreResizeGrowAllowsMoreAcquires(t *testing.T) {
	sem := newConcurrencySemaphore(1)
	ctx := context.Background()
	if err := sem.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- sem.acquire(ctx) }()
	select {
	case <-acquired:
		t.Fatal("second acquire succeeded before resize grew capacity")
	case <-time.After(20 * time.Millisecond):
	}
	sem.resize(2)
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second acquire did not unblock after growing capacity")
	}
}

func TestConcurrencySemaphoreResizeShrinkWithholdsReleasedSlot(t *testing.T) {
	sem := newConcurrencySemaphore(2)
	ctx := context.Background()
	if err := sem.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sem.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	// Shrink to 1 while both slots are held; nothing to withdraw immediately,
	// so the debt should be paid off by the next release instead of the one
	// after that.
	sem.resize(1)
	sem.release()
	sem.release()
	acquired := make(chan error, 1)
	go func() { acquired <- sem.acquire(ctx) }()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("acquire after shrink+two releases should have succeeded once")
	}
	second := make(chan error, 1)
	go func() { second <- sem.acquire(context.Background()) }()
	select {
	case <-second:
		t.Fatal("a second concurrent acquire succeeded after shrinking to 1")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestValidateSettingsUpdateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name   string
		update SettingsUpdate
	}{
		{"concurrency too low", SettingsUpdate{Concurrency: intPtr(0)}},
		{"concurrency too high", SettingsUpdate{Concurrency: intPtr(maxConcurrencyPermits + 1)}},
		{"no active agents", SettingsUpdate{ActiveAgents: agentsPtr()}},
		{"empty agent", SettingsUpdate{ActiveAgents: agentsPtr("   ")}},
		{"unknown agent provider", SettingsUpdate{ActiveAgents: agentsPtr("bogus")}},
		{"bad agent level", SettingsUpdate{ActiveAgents: agentsPtr("codex:extreme")}},
		{"one good one bad agent", SettingsUpdate{ActiveAgents: agentsPtr("codex", "bogus")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSettingsUpdate(agentRegistry(), tc.update); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGlorpApplySettingsConcurrencyTakesEffectLive(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{{Number: 1}, {Number: 2}}}}
	r := &fakeRunner{release: make(chan struct{}), dispatched: make(chan int, 2)}
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: time.Millisecond, Concurrency: 1, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: logs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	<-r.dispatched
	select {
	case <-r.dispatched:
		cancel()
		<-done
		t.Fatal("second issue dispatched before concurrency was raised")
	case <-time.After(20 * time.Millisecond):
	}

	snapshot, err := w.ApplySettings(ctx, SettingsUpdate{Concurrency: intPtr(2)})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Concurrency != 2 {
		t.Fatalf("snapshot concurrency = %d, want 2", snapshot.Concurrency)
	}
	select {
	case <-r.dispatched:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("second issue was not dispatched after raising concurrency")
	}
	close(r.release)
	cancel()
	<-done
}

func TestGlorpApplySettingsReadyStateAndAllowedCommenters(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{}}}
	r := &fakeRunner{release: make(chan struct{})}
	defer close(r.release)
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: logs, ReadyState: "Ready"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	commenters := []string{"alice", "bob"}
	snapshot, err := w.ApplySettings(ctx, SettingsUpdate{ReadyState: strPtr("Agent Ready"), AllowedCommenters: &commenters})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReadyState != "Agent Ready" {
		t.Fatalf("readyState = %q", snapshot.ReadyState)
	}
	if len(snapshot.AllowedCommenters) != 2 || snapshot.AllowedCommenters[0] != "alice" || snapshot.AllowedCommenters[1] != "bob" {
		t.Fatalf("allowedCommenters = %v", snapshot.AllowedCommenters)
	}

	cancel()
	<-done
}

func TestGlorpApplySettingsReadyStateDefaultReflectsUnsetFallback(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{}}}
	r := &fakeRunner{release: make(chan struct{})}
	defer close(r.release)
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: logs}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	snapshot, err := w.CurrentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReadyState != "" {
		t.Fatalf("readyState = %q, want unset", snapshot.ReadyState)
	}
	if snapshot.ReadyStateDefault != "Todo" {
		t.Fatalf("readyStateDefault = %q, want %q", snapshot.ReadyStateDefault, "Todo")
	}

	snapshot, err = w.ApplySettings(ctx, SettingsUpdate{ReadyState: strPtr("Agent Ready")})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReadyStateDefault != "Agent Ready" {
		t.Fatalf("readyStateDefault after configuring = %q, want %q", snapshot.ReadyStateDefault, "Agent Ready")
	}

	cancel()
	<-done
}

// TestGlorpApplySettingsFailsFastBeforeRunIsReady checks that a settings
// request made before Run starts fails with core.ErrNotReady, bounded by
// notReadyWait, instead of blocking forever on a channel Run isn't reading
// yet -- the hang reported in issue #579.
func TestGlorpApplySettingsFailsFastBeforeRunIsReady(t *testing.T) {
	w := &Glorp{notReadyWait: 20 * time.Millisecond}
	start := time.Now()
	_, err := w.ApplySettings(context.Background(), SettingsUpdate{})
	if !errors.Is(err, core.ErrNotReady) {
		t.Fatalf("ApplySettings error = %v, want core.ErrNotReady", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ApplySettings took %s, want it bounded by notReadyWait", elapsed)
	}
}

// TestGlorpHandleJobActionFailsFastBeforeRunIsReady mirrors
// TestGlorpApplySettingsFailsFastBeforeRunIsReady for the job-action path
// (issue #579).
func TestGlorpHandleJobActionFailsFastBeforeRunIsReady(t *testing.T) {
	w := &Glorp{notReadyWait: 20 * time.Millisecond}
	start := time.Now()
	err := w.handleJobAction(context.Background(), core.JobAction{Action: "retry", Target: "o/r", Number: 1})
	if !errors.Is(err, core.ErrNotReady) {
		t.Fatalf("handleJobAction error = %v, want core.ErrNotReady", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("handleJobAction took %s, want it bounded by notReadyWait", elapsed)
	}
}

// TestGlorpApplySettingsSucceedsOnceRunBecomesReady checks a settings
// request that arrives just before Run reaches readiness still succeeds once
// it gets there, rather than failing merely because it arrived first (issue
// #579).
func TestGlorpApplySettingsSucceedsOnceRunBecomesReady(t *testing.T) {
	dir := t.TempDir()
	src := &fakeSource{batches: [][]Issue{{}}}
	r := &fakeRunner{release: make(chan struct{})}
	defer close(r.release)
	logs := &syncBuffer{}
	w := &Glorp{Repo: "o/r", Interval: time.Hour, Concurrency: 1, StatePath: filepath.Join(dir, "state"), Issues: src, Runner: r, Out: logs, notReadyWait: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	if _, err := w.ApplySettings(ctx, SettingsUpdate{}); err != nil {
		t.Fatalf("ApplySettings = %v, want success once Run becomes ready", err)
	}

	cancel()
	<-done
}

func TestGlorpRunnerAppliesLiveAgentOverride(t *testing.T) {
	w := &Glorp{Runner: CommandRunner{Agent: "codex", Agents: []string{"codex"}}}
	if got := w.runner().(CommandRunner).AgentName(); got != "codex" {
		t.Fatalf("agent before override = %q", got)
	}
	override := []string{"claude/opus"}
	w.agentOverride.Store(&override)
	runner, ok := w.runner().(CommandRunner)
	if !ok {
		t.Fatal("runner() did not return a CommandRunner")
	}
	if got := runner.AgentName(); got != "claude/opus" {
		t.Fatalf("agent after override = %q, want claude/opus", got)
	}
}

// TestGlorpRunnerAppliesLiveMultiAgentOverride checks a multiselect override
// (issue #572) round robins across every agent it names, not just the first.
func TestGlorpRunnerAppliesLiveMultiAgentOverride(t *testing.T) {
	w := &Glorp{Runner: CommandRunner{Agent: "codex", Agents: []string{"codex"}}}
	override := []string{"codex", "claude/opus", "muse"}
	w.agentOverride.Store(&override)
	runner, ok := w.runner().(CommandRunner)
	if !ok {
		t.Fatal("runner() did not return a CommandRunner")
	}
	if got := strings.Join(runner.Agents, ","); got != "codex,claude/opus,muse" {
		t.Fatalf("runner.Agents = %q, want every overridden agent", got)
	}
}

func TestWebUIHandlesSettings(t *testing.T) {
	ui, err := webui.New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	var got SettingsUpdate
	ui.SetSettingsHandler(func(_ context.Context, update SettingsUpdate) (SettingsSnapshot, error) {
		got = update
		return SettingsSnapshot{Concurrency: 5, ReadyState: "Ready"}, nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(`{"concurrency":5}`))
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST settings = %d, body = %q", response.Code, response.Body.String())
	}
	if got.Concurrency == nil || *got.Concurrency != 5 {
		t.Fatalf("update.Concurrency = %v", got.Concurrency)
	}
	var snapshot SettingsSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Concurrency != 5 || snapshot.ReadyState != "Ready" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestWebUIRejectsUnavailableSettings(t *testing.T) {
	ui, err := webui.New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET settings = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestWebUIRejectsInvalidSettingsUpdate(t *testing.T) {
	ui, err := webui.New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	ui.SetSettingsHandler(func(_ context.Context, update SettingsUpdate) (SettingsSnapshot, error) {
		return SettingsSnapshot{}, nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(`{"unknown":true}`))
	response := httptest.NewRecorder()
	ui.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid settings = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func intPtr(v int) *int               { return &v }
func strPtr(v string) *string         { return &v }
func agentsPtr(v ...string) *[]string { return &v }

func TestGlorpAgentStillConfigured(t *testing.T) {
	commandRunner := CommandRunner{Agent: "codex", Agents: []string{"codex", "claude/opus:high"}}
	single := CommandRunner{Agent: "claude"}
	cases := []struct {
		name     string
		glorp    *Glorp
		override string
		agent    string
		want     bool
	}{
		{name: "listed", glorp: &Glorp{Runner: commandRunner}, agent: "codex", want: true},
		{name: "listed with spec", glorp: &Glorp{Runner: commandRunner}, agent: "claude/opus:high", want: true},
		{name: "retired", glorp: &Glorp{Runner: commandRunner}, agent: "claude", want: false},
		{name: "empty", glorp: &Glorp{Runner: commandRunner}, agent: "", want: false},
		{name: "single agent", glorp: &Glorp{Runner: single}, agent: "claude", want: true},
		{name: "single agent mismatch", glorp: &Glorp{Runner: single}, agent: "codex", want: false},
		{name: "override wins", glorp: &Glorp{Runner: commandRunner}, override: "claude", agent: "codex", want: false},
		{name: "override matches", glorp: &Glorp{Runner: commandRunner}, override: "claude", agent: "claude", want: true},
		{name: "unconfigured runner", glorp: &Glorp{Runner: &fakeSessionRunner{agent: "claude"}}, agent: "codex", want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.override != "" {
				override := []string{test.override}
				test.glorp.agentOverride.Store(&override)
			}
			if got := test.glorp.agentStillConfigured(test.agent); got != test.want {
				t.Fatalf("agentStillConfigured(%q) = %t, want %t", test.agent, got, test.want)
			}
		})
	}
}

// registryForSettings builds a registry holding a built-in agent and one an
// imaginary config file added, so the settings surfaces can be checked to
// report both.
func registryForSettings(t *testing.T) *agents.Registry {
	t.Helper()
	definition := func(name string) agents.Definition {
		return agents.Definition{
			Name: name, Binary: name,
			Args:    agents.Args{Run: []agents.Fragment{{Args: []string{"{prompt}"}}}, Resume: []agents.Fragment{{Args: []string{"{prompt}"}}}},
			Session: agents.Session{Assign: agents.AssignNone},
			Output:  agents.Output{Format: agents.FormatText},
		}
	}
	codex := definition("codex")
	codex.Levels = agents.NewAllowList("low", "medium", "high")
	muse := definition("muse")
	muse.Models = agents.NewAllowList("muse-1", "muse-2")
	registry, err := agents.NewRegistry(codex, muse)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// TestSettingsSnapshotListsEveryRegisteredAgent checks the settings API
// reports the registry rather than the agents this run happens to dispatch
// with, which is what makes a .glorp.config.json agent selectable in the
// dashboard with no code change (issue #489).
func TestSettingsSnapshotListsEveryRegisteredAgent(t *testing.T) {
	w := &Glorp{
		Registry: registryForSettings(t),
		Runner:   CommandRunner{Agent: "codex", Agents: []string{"codex"}},
	}
	snapshot := w.settingsSnapshot()
	if got := strings.Join(snapshot.Agents, ","); got != "codex,muse" {
		t.Fatalf("agents = %q, want every registered agent", got)
	}
	if got := strings.Join(snapshot.ConfiguredAgents, ","); got != "codex" {
		t.Fatalf("configuredAgents = %q, want only what --agent selected", got)
	}
	if len(snapshot.AgentOptions) != 2 {
		t.Fatalf("agentOptions = %v, want one per registered agent", snapshot.AgentOptions)
	}
	if got := strings.Join(snapshot.AgentOptions[0].Levels, ","); got != "low,medium,high" {
		t.Fatalf("codex levels = %q", got)
	}
	if got := strings.Join(snapshot.AgentOptions[1].Models, ","); got != "muse-1,muse-2" {
		t.Fatalf("muse models = %q", got)
	}
}

// TestSettingsValidatesAgainstTheRunsRegistry checks the live --agent override
// accepts a config-defined agent and rejects an unknown one with the list the
// CLI would print, so the dashboard and the settings API answer a typo the
// same way the command line does.
func TestSettingsValidatesAgainstTheRunsRegistry(t *testing.T) {
	registry := registryForSettings(t)
	if err := validateSettingsUpdate(registry, SettingsUpdate{ActiveAgents: agentsPtr("muse/muse-2")}); err != nil {
		t.Fatalf("config-defined agent rejected: %v", err)
	}
	err := validateSettingsUpdate(registry, SettingsUpdate{ActiveAgents: agentsPtr("claude")})
	if err == nil {
		t.Fatal("expected an agent outside the run's registry to be rejected")
	}
	if !strings.Contains(err.Error(), "known agents are codex, muse") {
		t.Fatalf("error = %v, want it to list the registry's agents", err)
	}
	if err := validateSettingsUpdate(registry, SettingsUpdate{ActiveAgents: agentsPtr("muse/muse-9")}); err == nil {
		t.Fatal("expected a model outside the agent's list to be rejected")
	}
}

// TestAgentStillConfiguredIgnoresTheRegistry checks the resume path keeps
// asking what --agent selected rather than what the registry defines (issue
// #489). An agent that is still registered but is no longer dispatched to must
// not hold a persisted session, and an agent a config file dropped entirely
// must not either.
func TestAgentStillConfiguredIgnoresTheRegistry(t *testing.T) {
	w := &Glorp{
		Registry: registryForSettings(t),
		Runner:   CommandRunner{Agent: "codex", Agents: []string{"codex"}},
	}
	if w.agentStillConfigured("muse") {
		t.Fatal("a registered agent absent from --agent should not keep a session")
	}
	if !w.agentStillConfigured("codex") {
		t.Fatal("the configured agent should keep its session")
	}
	// A config-defined agent that disappeared from config is gone from the
	// registry too, and is likewise not configured.
	dropped := &Glorp{Runner: CommandRunner{Agent: "codex", Agents: []string{"codex"}}}
	if dropped.agentStillConfigured("muse") {
		t.Fatal("an agent no longer in config should not keep a session")
	}
	// It is still dispatchable while --agent names it, registry or not: the
	// registry rejects it at parse time, not here.
	kept := &Glorp{Runner: CommandRunner{Agent: "muse", Agents: []string{"muse"}}}
	if !kept.agentStillConfigured("muse") {
		t.Fatal("an agent --agent still names should keep its session")
	}
}
