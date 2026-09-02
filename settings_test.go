package main

import (
	"github.com/lsegal/glorp/webui"

	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		{"empty agent", SettingsUpdate{Agent: strPtr("   ")}},
		{"unknown agent provider", SettingsUpdate{Agent: strPtr("bogus")}},
		{"bad agent level", SettingsUpdate{Agent: strPtr("codex:extreme")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSettingsUpdate(tc.update); err == nil {
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

func TestGlorpRunnerAppliesLiveAgentOverride(t *testing.T) {
	w := &Glorp{Runner: CommandRunner{Agent: "codex", Agents: []string{"codex"}}}
	if got := w.runner().(CommandRunner).AgentName(); got != "codex" {
		t.Fatalf("agent before override = %q", got)
	}
	override := "claude/opus"
	w.agentOverride.Store(&override)
	runner, ok := w.runner().(CommandRunner)
	if !ok {
		t.Fatal("runner() did not return a CommandRunner")
	}
	if got := runner.AgentName(); got != "claude/opus" {
		t.Fatalf("agent after override = %q, want claude/opus", got)
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

func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

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
				override := test.override
				test.glorp.agentOverride.Store(&override)
			}
			if got := test.glorp.agentStillConfigured(test.agent); got != test.want {
				t.Fatalf("agentStillConfigured(%q) = %t, want %t", test.agent, got, test.want)
			}
		})
	}
}
