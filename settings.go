package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// maxConcurrencyPermits bounds how high --concurrency can be raised at
// runtime (issue #341). It is far above any concurrency a real instance
// would use; it only exists so concurrencySemaphore can pre-allocate a
// fixed-capacity channel instead of an unbounded one.
const maxConcurrencyPermits = 256

// concurrencySemaphore is a counting semaphore whose capacity can change
// while acquire/release calls are in flight, which a plain buffered channel
// cannot do. Live settings changes (issue #341) need to grow or shrink the
// number of concurrently dispatched agents without restarting glorp.
type concurrencySemaphore struct {
	mu     sync.Mutex
	limit  int
	debt   int
	tokens chan struct{}
}

func newConcurrencySemaphore(limit int) *concurrencySemaphore {
	s := &concurrencySemaphore{tokens: make(chan struct{}, maxConcurrencyPermits)}
	s.resize(limit)
	return s
}

// acquire blocks until a slot is available or ctx is done.
func (s *concurrencySemaphore) acquire(ctx context.Context) error {
	select {
	case <-s.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns a slot. A shrink that happened while the slot was held
// withdraws it instead of returning it to the pool.
func (s *concurrencySemaphore) release() {
	s.mu.Lock()
	if s.debt > 0 {
		s.debt--
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.tokens <- struct{}{}
}

// resize changes the number of available slots. Growing mints new tokens
// immediately; shrinking withdraws idle tokens first and records the rest as
// debt to withdraw from future releases, so slots already in use are never
// revoked out from under a running task.
func (s *concurrencySemaphore) resize(newLimit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delta := newLimit - s.limit
	s.limit = newLimit
	if delta > 0 {
		for i := 0; i < delta; i++ {
			s.tokens <- struct{}{}
		}
		return
	}
	for i := 0; i < -delta; i++ {
		select {
		case <-s.tokens:
		default:
			s.debt++
		}
	}
}

// SettingsUpdate carries a partial update to glorp's live-editable runtime
// settings (issue #341). Nil fields are left unchanged.
type SettingsUpdate struct {
	Concurrency       *int      `json:"concurrency,omitempty"`
	ReadyState        *string   `json:"readyState,omitempty"`
	AllowedCommenters *[]string `json:"allowedCommenters,omitempty"`
	Agent             *string   `json:"agent,omitempty"`
}

// SettingsSnapshot reports the settings a running glorp instance can change
// on the fly, and their current values.
type SettingsSnapshot struct {
	Concurrency       int      `json:"concurrency"`
	ReadyState        string   `json:"readyState"`
	AllowedCommenters []string `json:"allowedCommenters"`
	Agent             string   `json:"agent"`
	Agents            []string `json:"agents"`
}

type settingsRequest struct {
	update SettingsUpdate
	done   chan settingsResult
}

type settingsResult struct {
	snapshot SettingsSnapshot
	err      error
}

func (w *Glorp) settingsRequestsChan() chan settingsRequest {
	w.settingsOnce.Do(func() { w.settingsRequests = make(chan settingsRequest) })
	return w.settingsRequests
}

// ApplySettings validates and applies update, returning the resulting
// settings snapshot. It hands the update to the running Run loop, which
// owns the mutated fields, so it only takes effect once Run is looping.
func (w *Glorp) ApplySettings(ctx context.Context, update SettingsUpdate) (SettingsSnapshot, error) {
	request := settingsRequest{update: update, done: make(chan settingsResult, 1)}
	select {
	case w.settingsRequestsChan() <- request:
	case <-ctx.Done():
		return SettingsSnapshot{}, ctx.Err()
	}
	select {
	case result := <-request.done:
		return result.snapshot, result.err
	case <-ctx.Done():
		return SettingsSnapshot{}, ctx.Err()
	}
}

// CurrentSettings reports the live settings snapshot without changing
// anything.
func (w *Glorp) CurrentSettings(ctx context.Context) (SettingsSnapshot, error) {
	return w.ApplySettings(ctx, SettingsUpdate{})
}

// validateSettingsUpdate checks update in isolation, before it reaches the
// Run loop, so malformed requests fail fast with a useful message instead of
// silently corrupting state owned by that loop.
func validateSettingsUpdate(update SettingsUpdate) error {
	if update.Concurrency != nil && (*update.Concurrency < 1 || *update.Concurrency > maxConcurrencyPermits) {
		return fmt.Errorf("concurrency must be between 1 and %d", maxConcurrencyPermits)
	}
	if update.Agent != nil {
		if strings.TrimSpace(*update.Agent) == "" {
			return fmt.Errorf("agent must not be empty")
		}
		if _, err := parseAgentSpec(*update.Agent); err != nil {
			return err
		}
	}
	return nil
}

// applySettingsRequest mutates the fields owned by the Run loop and returns
// the resulting snapshot. It must only be called from the Run loop's
// goroutine, the same rule handleJobAction's callers already follow.
func (w *Glorp) applySettingsRequest(update SettingsUpdate, sem *concurrencySemaphore) settingsResult {
	if err := validateSettingsUpdate(update); err != nil {
		return settingsResult{err: err}
	}
	if update.Concurrency != nil {
		w.Concurrency = *update.Concurrency
		sem.resize(w.Concurrency)
	}
	if update.ReadyState != nil {
		w.ReadyState = strings.TrimSpace(*update.ReadyState)
	}
	if update.AllowedCommenters != nil {
		w.AllowedCommenters = splitAllowedCommenters(strings.Join(*update.AllowedCommenters, ","))
	}
	if update.Agent != nil {
		agent := strings.TrimSpace(*update.Agent)
		w.agentOverride.Store(&agent)
	}
	return settingsResult{snapshot: w.settingsSnapshot()}
}

// settingsSnapshot reads the fields applySettingsRequest owns. Like that
// function, it must only be called from the Run loop's goroutine.
func (w *Glorp) settingsSnapshot() SettingsSnapshot {
	var agent string
	var agents []string
	if runner, ok := w.Runner.(CommandRunner); ok {
		agent, agents = runner.Agent, append([]string(nil), runner.Agents...)
	}
	if override := w.agentOverride.Load(); override != nil && *override != "" {
		agent = *override
	}
	return SettingsSnapshot{
		Concurrency:       w.Concurrency,
		ReadyState:        w.ReadyState,
		AllowedCommenters: append([]string(nil), w.AllowedCommenters...),
		Agent:             agent,
		Agents:            agents,
	}
}

// runner returns the AgentRunner new dispatches should use, applying any
// live agent override (issue #341) on top of the configured CommandRunner.
// Unlike the fields applySettingsRequest owns, this is read from goroutines
// spawned by the Run loop as well as the loop itself, so the override is
// stored behind an atomic pointer rather than left to single-goroutine
// ownership.
func (w *Glorp) runner() AgentRunner {
	override := w.agentOverride.Load()
	if override == nil || *override == "" {
		return w.Runner
	}
	if runner, ok := w.Runner.(CommandRunner); ok {
		runner.Agent = *override
		runner.Agents = []string{*override}
		return runner
	}
	return w.Runner
}
