package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Issue struct {
	Number        int               `json:"number"`
	Repository    string            `json:"repository,omitempty"`
	Title         string            `json:"title,omitempty"`
	Body          string            `json:"body,omitempty"`
	State         string            `json:"state,omitempty"`
	CreatedAt     time.Time         `json:"createdAt,omitempty"`
	Labels        []IssueLabel      `json:"labels,omitempty"`
	DependsOn     []IssueDependency `json:"dependsOn,omitempty"`
	ProjectStatus string            `json:"projectStatus,omitempty"`
}

func issueRepository(target string, issue Issue) string {
	if issue.Repository != "" {
		return issue.Repository
	}
	parsed, err := parseTarget(target)
	if err == nil && parsed.repo != "" {
		return parsed.repo
	}
	return target
}

type IssueLabel struct {
	Name string `json:"name"`
}
type IssueDependency struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}
type IssueSource interface {
	ListIssues(context.Context, string) ([]Issue, error)
}
type LabelEnsurer interface {
	EnsureLabels(context.Context, string) error
}
type IssueLabeler interface {
	SetIssueLabel(context.Context, string, int, bool) error
}
type IssueStatuser interface {
	SetIssueStatus(context.Context, string, int, string) error
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
	Labels      LabelEnsurer
	Status      IssueStatuser
	logMu       sync.Mutex
}

const agentStartedLabel = "agent-started"

type workState struct {
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
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
	if _, err := parseTarget(w.Repo); err != nil {
		return err
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
	if w.Labels != nil {
		if err := w.Labels.EnsureLabels(ctx, w.Repo); err != nil {
			return err
		}
		w.logf("ensured agent labels exist")
	}
	work, err := loadWorkState(w.StatePath)
	if err != nil {
		return err
	}
	seen := make(map[int]bool, len(work))
	for number := range work {
		seen[number] = true
	}
	w.logf("watching %s (poll every %s, concurrency %d; %d handled issue(s) loaded)", w.Repo, w.Interval, w.Concurrency, len(seen))
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	var tasks taskState
	var workMu sync.Mutex
	active := make(map[int]string)
	labeler, _ := w.Labels.(IssueLabeler)
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
			if blocked, reason := issueBlocked(issue); blocked {
				w.logf("issue #%d not picked up: %s", issue.Number, reason)
				continue
			}
			workMu.Lock()
			_, isActive := active[issue.Number]
			wasActive := work[issue.Number].Status == "active"
			workMu.Unlock()
			if issue.Number > 0 && shouldDispatchIssue(w.Repo, issue, isActive, wasActive, seen[issue.Number]) {
				seen[issue.Number] = true
				newIssues = append(newIssues, issue)
			}
		}
		workMu.Lock()
		err = saveWorkState(w.StatePath, work)
		workMu.Unlock()
		if err != nil {
			w.logf("poll #%d failed while saving state: %v", n, err)
			return err
		}
		if len(newIssues) == 0 {
			w.logf("poll #%d complete; no new issues (tasks: %d running, %d queued)", n, running, queued)
			return nil
		}
		w.logf("poll #%d discovered %d new issue(s): %s", n, len(newIssues), issueNumbers(newIssues))
		for _, issue := range newIssues {
			session, err := newSessionID()
			if err != nil {
				return err
			}
			if labeler != nil {
				if err := labeler.SetIssueLabel(ctx, issueRepository(w.Repo, issue), issue.Number, true); err != nil {
					return err
				}
			}
			if w.Status != nil {
				if err := w.Status.SetIssueStatus(ctx, w.Repo, issue.Number, "In Progress"); err != nil {
					return err
				}
			}
			workMu.Lock()
			active[issue.Number] = session
			work[issue.Number] = workState{Status: "active", SessionID: session}
			err = saveWorkState(w.StatePath, work)
			workMu.Unlock()
			if err != nil {
				return err
			}
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
					if w.Status != nil {
						if statusErr := w.Status.SetIssueStatus(context.Background(), w.Repo, i.Number, "Todo"); statusErr != nil {
							w.logf("issue #%d failed to reset project status: %v", i.Number, statusErr)
						}
					}
					if labeler != nil {
						_ = labeler.SetIssueLabel(context.Background(), issueRepository(w.Repo, i), i.Number, false)
					}
					workMu.Lock()
					delete(active, i.Number)
					work[i.Number] = workState{Status: "failed", SessionID: work[i.Number].SessionID}
					_ = saveWorkState(w.StatePath, work)
					workMu.Unlock()
					tasks.mu.Lock()
					tasks.running--
					tasks.failed++
					running, queued, completed, failed := tasks.running, tasks.queued, tasks.completed, tasks.failed
					tasks.mu.Unlock()
					w.logf("issue #%d failed: %v (tasks: %d running, %d queued, %d completed, %d failed)", i.Number, err, running, queued, completed, failed)
				} else {
					if w.Status != nil {
						if statusErr := w.Status.SetIssueStatus(context.Background(), w.Repo, i.Number, "Done"); statusErr != nil {
							w.logf("issue #%d failed to update project status: %v", i.Number, statusErr)
						}
					}
					if labeler != nil {
						_ = labeler.SetIssueLabel(context.Background(), issueRepository(w.Repo, i), i.Number, false)
					}
					workMu.Lock()
					delete(active, i.Number)
					work[i.Number] = workState{Status: "completed", SessionID: work[i.Number].SessionID}
					_ = saveWorkState(w.StatePath, work)
					workMu.Unlock()
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

func issueBlocked(issue Issue) (bool, string) {
	blocked := make([]string, 0)
	for _, dependency := range issue.DependsOn {
		if !strings.EqualFold(dependency.State, "closed") {
			if dependency.State == "" {
				blocked = append(blocked, fmt.Sprintf("depends on #%d", dependency.Number))
			} else {
				blocked = append(blocked, fmt.Sprintf("depends on #%d (%s)", dependency.Number, strings.ToLower(dependency.State)))
			}
		}
	}
	if len(blocked) == 0 {
		return false, ""
	}
	return true, strings.Join(blocked, ", ")
}

func issueNumbers(issues []Issue) string {
	numbers := make([]string, len(issues))
	for i, issue := range issues {
		numbers[i] = fmt.Sprintf("#%d", issue.Number)
	}
	return strings.Join(numbers, ", ")
}
func hasLabel(issue Issue, name string) bool {
	for _, label := range issue.Labels {
		if label.Name == name {
			return true
		}
	}
	return false
}

func shouldDispatchIssue(repo string, issue Issue, isActive, wasActive, seen bool) bool {
	if isActive {
		return false
	}
	if wasActive || !seen {
		return true
	}
	if isProjectTarget(repo) {
		return issue.ProjectStatus == "In Progress"
	}
	return hasLabel(issue, agentStartedLabel)
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create agent session: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func parseIssues(data []byte) ([]Issue, error) {
	var issues []Issue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("decode issues: %w", err)
	}
	return issues, nil
}

type projectItem struct {
	ID      string          `json:"id"`
	Content *projectContent `json:"content"`
	Status  string          `json:"status"`
}

type projectContent struct {
	Issue
	Type string `json:"type"`
}

type projectList struct {
	Items []projectItem `json:"items"`
}

func decodeProjectIssues(data []byte, err error) ([]Issue, error) {
	items, decodeErr := decodeProjectItems(data, err)
	if decodeErr != nil {
		return nil, decodeErr
	}
	issues := make([]Issue, 0, len(items))
	for _, item := range items {
		if item.Content != nil && item.Content.Type == "Issue" {
			issue := item.Content.Issue
			issue.ProjectStatus = item.Status
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

func isProjectTarget(repo string) bool {
	target, err := parseTarget(repo)
	return err == nil && target.isProject
}

func decodeProjectItems(data []byte, err error) ([]projectItem, error) {
	if err != nil {
		detail := strings.TrimSpace(string(data))
		if strings.Contains(detail, "missing required scopes") && strings.Contains(detail, "read:project") {
			return nil, fmt.Errorf("list project items: %w; authenticate with the read:project scope using `gh auth refresh -s read:project`", err)
		}
		if detail != "" {
			return nil, fmt.Errorf("list project items: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("list project items: %w", err)
	}
	var result projectList
	if err := json.Unmarshal(data, &result); err == nil && result.Items != nil {
		return result.Items, nil
	}
	var items []projectItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode project items: %w", err)
	}
	return items, nil
}

func decodeProjectFields(data []byte, err error) ([]projectField, error) {
	if err != nil {
		return nil, fmt.Errorf("list project fields: %w", err)
	}
	var result projectFields
	if decodeErr := json.Unmarshal(data, &result); decodeErr == nil && result.Fields != nil {
		return result.Fields, nil
	}
	var fields []projectField
	if decodeErr := json.Unmarshal(data, &fields); decodeErr != nil {
		return nil, fmt.Errorf("decode project fields: %w", decodeErr)
	}
	return fields, nil
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
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	s := make(map[int]bool, len(raw))
	for key, value := range raw {
		var number int
		if _, err := fmt.Sscanf(key, "%d", &number); err != nil {
			return nil, fmt.Errorf("decode state issue %q: %w", key, err)
		}
		var present bool
		if err := json.Unmarshal(value, &present); err == nil {
			s[number] = present
			continue
		}
		var state workState
		if err := json.Unmarshal(value, &state); err != nil {
			return nil, fmt.Errorf("decode state issue %q: %w", key, err)
		}
		s[number] = state.Status != ""
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

func loadWorkState(path string) (map[int]workState, error) {
	result := make(map[int]workState)
	if path == "" {
		return result, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	for key, value := range raw {
		var number int
		if _, err := fmt.Sscanf(key, "%d", &number); err != nil {
			return nil, fmt.Errorf("decode state issue %q: %w", key, err)
		}
		var legacy bool
		if json.Unmarshal(value, &legacy) == nil {
			if legacy {
				result[number] = workState{Status: "completed"}
			}
			continue
		}
		var state workState
		if err := json.Unmarshal(value, &state); err != nil {
			return nil, fmt.Errorf("decode state issue %q: %w", key, err)
		}
		result[number] = state
	}
	return result, nil
}

func saveWorkState(path string, state map[int]workState) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
