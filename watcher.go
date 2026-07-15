package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title,omitempty"`
	State     string    `json:"state,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}
type IssueSource interface {
	ListIssues(context.Context, string) ([]Issue, error)
}
type AgentRunner interface {
	Run(context.Context, Issue) error
}
type Watcher struct {
	Repo        string
	Interval    time.Duration
	Concurrency int
	StatePath   string
	Issues      IssueSource
	Runner      AgentRunner
	Out         io.Writer
	logMu       sync.Mutex
}

type taskState struct {
	mu        sync.Mutex
	running   int
	queued    int
	completed int
	failed    int
}

func (s *taskState) snapshot() (running, queued, completed, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.queued, s.completed, s.failed
}

func (w *Watcher) logf(format string, args ...interface{}) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	fmt.Fprintf(w.Out, "%s "+format+"\n", append([]interface{}{time.Now().Format("2006-01-02 15:04:05")}, args...)...)
}

func (w *Watcher) Run(ctx context.Context) error {
	startedAt := time.Now()
	if !validRepo(w.Repo) {
		return fmt.Errorf("repository must be OWNER/REPO")
	}
	if w.Interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}
	if w.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be positive")
	}
	if w.Out == nil {
		w.Out = io.Discard
	}
	seen, err := loadState(w.StatePath)
	if err != nil {
		return err
	}
	w.logf("watching %s (poll every %s, concurrency %d; %d handled issue(s) loaded)", w.Repo, w.Interval, w.Concurrency, len(seen))
	first := len(seen) == 0
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	var tasks taskState
	pollNumber := 0
	poll := func() error {
		pollNumber++
		n := pollNumber
		running, queued, completed, failed := tasks.snapshot()
		w.logf("poll #%d started (tasks: %d running, %d queued, %d completed, %d failed)", n, running, queued, completed, failed)
		issues, err := w.Issues.ListIssues(ctx, w.Repo)
		if err != nil {
			w.logf("poll #%d failed while listing issues: %v", n, err)
			return err
		}
		w.logf("poll #%d found %d open issue(s)", n, len(issues))
		newIssues := make([]Issue, 0)
		for _, issue := range issues {
			// Issues that were already open when the watcher started are not
			// part of this watcher's workload. In particular, don't add them to
			// seen: the state file represents issues this process has observed,
			// not a snapshot of all existing issues.
			if issue.Number > 0 &&
				(issue.CreatedAt.IsZero() || !issue.CreatedAt.Before(startedAt)) &&
				!seen[issue.Number] {
				seen[issue.Number] = true
				newIssues = append(newIssues, issue)
			}
		}
		if err := saveState(w.StatePath, seen); err != nil {
			w.logf("poll #%d failed while saving state: %v", n, err)
			return err
		}
		if first {
			first = false
			w.logf("poll #%d established the baseline; %d issue(s) marked handled", n, len(newIssues))
			return nil
		}
		if len(newIssues) == 0 {
			w.logf("poll #%d complete; no new issues (tasks: %d running, %d queued)", n, running, queued)
			return nil
		}
		w.logf("poll #%d discovered %d new issue(s): %s", n, len(newIssues), issueNumbers(newIssues))
		for _, issue := range newIssues {
			tasks.mu.Lock()
			tasks.queued++
			queued = tasks.queued
			running = tasks.running
			tasks.mu.Unlock()
			w.logf("issue #%d queued (tasks: %d running, %d queued)", issue.Number, running, queued)
			select {
			case sem <- struct{}{}:
				tasks.mu.Lock()
				tasks.queued--
				tasks.running++
				queued = tasks.queued
				running = tasks.running
				tasks.mu.Unlock()
			case <-ctx.Done():
				tasks.mu.Lock()
				tasks.queued--
				tasks.mu.Unlock()
				return ctx.Err()
			}
			startedRunning, startedQueued := running, queued
			wg.Add(1)
			go func(i Issue, running, queued int) {
				defer wg.Done()
				defer func() { <-sem }()
				w.logf("issue #%d started (tasks: %d running, %d queued)", i.Number, running, queued)
				if err := w.Runner.Run(ctx, i); err != nil {
					tasks.mu.Lock()
					tasks.running--
					tasks.failed++
					running, queued, completed, failed := tasks.running, tasks.queued, tasks.completed, tasks.failed
					tasks.mu.Unlock()
					w.logf("issue #%d failed: %v (tasks: %d running, %d queued, %d completed, %d failed)", i.Number, err, running, queued, completed, failed)
				} else {
					tasks.mu.Lock()
					tasks.running--
					tasks.completed++
					running, queued, completed, failed := tasks.running, tasks.queued, tasks.completed, tasks.failed
					tasks.mu.Unlock()
					w.logf("issue #%d completed (tasks: %d running, %d queued, %d completed, %d failed)", i.Number, running, queued, completed, failed)
				}
			}(issue, startedRunning, startedQueued)
		}
		running, queued, _, _ = tasks.snapshot()
		w.logf("poll #%d complete; dispatched %d issue(s) (tasks: %d running, %d queued)", n, len(newIssues), running, queued)
		return nil
	}
	if err := poll(); err != nil {
		if ctx.Err() != nil {
			wg.Wait()
			w.logf("stopped during initial poll")
			return nil
		}
		return err
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logf("shutdown requested; waiting for running tasks to finish")
			wg.Wait()
			running, queued, completed, failed := tasks.snapshot()
			w.logf("stopped (tasks: %d running, %d queued, %d completed, %d failed)", running, queued, completed, failed)
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				if ctx.Err() != nil {
					w.logf("shutdown requested during poll; waiting for running tasks to finish")
					wg.Wait()
					w.logf("stopped")
					return nil
				}
				w.logf("poll #%d error: %v; will retry in %s", pollNumber, err, w.Interval)
			}
		}
	}
}

func issueNumbers(issues []Issue) string {
	numbers := make([]string, len(issues))
	for i, issue := range issues {
		numbers[i] = fmt.Sprintf("#%d", issue.Number)
	}
	return strings.Join(numbers, ", ")
}
func parseIssues(data []byte) ([]Issue, error) {
	var issues []Issue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("decode issues: %w", err)
	}
	return issues, nil
}
func loadState(path string) (map[int]bool, error) {
	if path == "" {
		return map[int]bool{}, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[int]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s map[int]bool
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if s == nil {
		s = map[int]bool{}
	}
	return s, nil
}
func saveState(path string, seen map[int]bool) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
