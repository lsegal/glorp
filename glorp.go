package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stateFilePollInterval = 100 * time.Millisecond
	stateReloadDebounce   = 5 * time.Second
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
	ProjectItemID string            `json:"-"`
	Target        string            `json:"-"`
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
	SetIssueStatus(context.Context, string, Issue, string) error
}
type AgentRunner interface {
	Run(context.Context, Issue) error
}
type AgentOutputRunner interface {
	RunWithOutput(context.Context, Issue, io.Writer) error
}
type UIReporter interface {
	Snapshot(GlorpSnapshot)
	Log(string)
}
type Glorp struct {
	Repo        string
	Targets     []string
	Interval    time.Duration
	UseWebhooks bool
	Events      <-chan WebhookEvent
	Concurrency int
	StatePath   string
	Issues      IssueSource
	Runner      AgentRunner
	Out         io.Writer
	Labels      LabelEnsurer
	Status      IssueStatuser
	UI          UIReporter
	Quota       func(context.Context) string
	logMu       sync.Mutex
}

const agentStartedLabel = "agent-started"

type workState struct {
	Status    string `json:"status"`
	SessionID string `json:"sessionId,omitempty"`
}

func issueKey(issue Issue) string {
	target := issue.Target
	if target == "" {
		target = issue.Repository
	}
	return target + "#" + strconv.Itoa(issue.Number)
}

type taskState struct {
	mu        sync.Mutex
	running   int
	queued    int
	completed int
	failed    int
}

type jobOutputWriter struct {
	write func(string)
}

func (w jobOutputWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.write(string(p))
	}
	return len(p), nil
}

func (s *taskState) snapshot() (running, queued, completed, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.queued, s.completed, s.failed
}

func (w *Glorp) logf(format string, args ...interface{}) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	line := fmt.Sprintf("%s "+format, append([]interface{}{time.Now().Format("2006-01-02 15:04:05")}, args...)...)
	fmt.Fprintln(w.Out, line)
	if w.UI != nil {
		w.UI.Log(line)
	}
}

func (w *Glorp) Run(ctx context.Context) error {
	targets := append([]string(nil), w.Targets...)
	if len(targets) == 0 && w.Repo != "" {
		targets = []string{w.Repo}
	}
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	for _, target := range targets {
		if _, err := parseTarget(target); err != nil {
			return err
		}
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
		for _, target := range targets {
			if err := w.Labels.EnsureLabels(ctx, target); err != nil {
				return err
			}
		}
		w.logf("ensured agent labels exist")
	}
	watchCtx, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	stateChanges := watchStateFile(watchCtx, w.StatePath)
	work, err := loadScopedWorkState(w.StatePath, targets)
	if err != nil {
		return err
	}
	labeler, _ := w.Labels.(IssueLabeler)
	if err := w.resetFailedWork(context.Background(), work, labeler); err != nil {
		return err
	}
	seen := make(map[string]bool, len(work))
	for key := range work {
		seen[key] = true
	}
	w.logf("watching %s (poll every %s, concurrency %d; %d handled issue(s) loaded)", strings.Join(targets, ", "), w.Interval, w.Concurrency, len(seen))
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	var tasks taskState
	var workMu sync.Mutex
	active := make(map[string]string)
	jobs := make(map[string]JobSnapshot)
	checkoutDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get agent checkout directory: %w", err)
	}
	issueCounts := make(map[string]int)
	var jobMu sync.Mutex
	publish := func() {
		if w.UI == nil {
			return
		}
		running, queued, completed, failed := tasks.snapshot()
		jobMu.Lock()
		list := make([]JobSnapshot, 0, len(jobs))
		for _, job := range jobs {
			list = append(list, job)
		}
		counts := make(map[string]int, len(issueCounts))
		for target, count := range issueCounts {
			counts[target] = count
		}
		jobMu.Unlock()
		slices.SortFunc(list, func(a, b JobSnapshot) int { return b.Started.Compare(a.Started) })
		if len(list) > maxVisibleJobs {
			list = list[:maxVisibleJobs]
		}
		quota := ""
		if w.Quota != nil {
			quota = w.Quota(ctx)
		}
		w.UI.Snapshot(GlorpSnapshot{Targets: targets, IssueCounts: counts, Running: running, Queued: queued, Completed: completed, Failed: failed, Concurrency: w.Concurrency, Interval: w.Interval, UseWebhooks: w.UseWebhooks, WebhookOnline: w.UseWebhooks, Quota: quota, Jobs: list})
	}
	pollNumber := 0
	poll := func() error {
		pollNumber++
		n := pollNumber
		running, queued, completed, failed := tasks.snapshot()
		w.logf("poll #%d started (tasks: %d running, %d queued, %d completed, %d failed)", n, running, queued, completed, failed)
		issues := make([]Issue, 0)
		for _, target := range targets {
			batch, err := w.Issues.ListIssues(ctx, target)
			if err != nil {
				w.logf("poll #%d failed while listing %s: %v", n, target, err)
				return err
			}
			jobMu.Lock()
			issueCounts[target] = len(batch)
			jobMu.Unlock()
			for i := range batch {
				batch[i].Target = target
				if batch[i].Repository == "" {
					batch[i].Repository = issueRepository(target, batch[i])
				}
				issues = append(issues, batch[i])
			}
		}
		w.logf("poll #%d found %d open issue(s)", n, len(issues))
		newIssues := make([]Issue, 0)
		for _, issue := range issues {
			if blocked, reason := issueBlocked(issue); blocked {
				w.logf("issue #%d not picked up: %s", issue.Number, reason)
				continue
			}
			key := issueKey(issue)
			workMu.Lock()
			_, isActive := active[key]
			wasActive := work[key].Status == "active"
			workMu.Unlock()
			if issue.Number > 0 && shouldDispatchIssue(issue.Target, issue, isActive, wasActive, seen[key]) {
				seen[key] = true
				newIssues = append(newIssues, issue)
			}
		}
		workMu.Lock()
		err = saveScopedWorkState(w.StatePath, work, targets)
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
			if labeler != nil && !isProjectTarget(issue.Target) {
				if err := labeler.SetIssueLabel(ctx, issueRepository(issue.Target, issue), issue.Number, true); err != nil {
					return err
				}
			}
			if w.Status != nil {
				if err := w.Status.SetIssueStatus(ctx, issue.Target, issue, "In Progress"); err != nil {
					return err
				}
			}
			workMu.Lock()
			key := issueKey(issue)
			active[key] = session
			jobMu.Lock()
			jobs[key] = JobSnapshot{Number: issue.Number, Title: issue.Title, Status: "queued", CheckoutDirectory: checkoutDirectory, SessionID: session, Started: time.Now()}
			jobMu.Unlock()
			work[key] = workState{Status: "active", SessionID: session}
			err = saveScopedWorkState(w.StatePath, work, targets)
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
			publish()
			select {
			case sem <- struct{}{}:
				tasks.mu.Lock()
				tasks.queued--
				tasks.running++
				queued = tasks.queued
				running = tasks.running
				tasks.mu.Unlock()
				jobMu.Lock()
				job := jobs[issueKey(issue)]
				job.Status = "active"
				jobs[issueKey(issue)] = job
				jobMu.Unlock()
				publish()
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
				jobOutput := jobOutputWriter{write: func(text string) {
					jobMu.Lock()
					job := jobs[issueKey(i)]
					job.Log += text
					jobs[issueKey(i)] = job
					jobMu.Unlock()
					publish()
				}}
				var runErr error
				if w.UI != nil {
					if runner, ok := w.Runner.(AgentOutputRunner); ok {
						runErr = runner.RunWithOutput(ctx, i, jobOutput)
					} else {
						runErr = w.Runner.Run(ctx, i)
					}
				} else {
					runErr = w.Runner.Run(ctx, i)
				}
				if runErr != nil {
					if w.Status != nil {
						if statusErr := w.Status.SetIssueStatus(context.Background(), i.Target, i, "Todo"); statusErr != nil {
							w.logf("issue #%d failed to reset project status: %v", i.Number, statusErr)
						}
					}
					if labeler != nil && !isProjectTarget(i.Target) {
						_ = labeler.SetIssueLabel(context.Background(), issueRepository(i.Target, i), i.Number, false)
					}
					workMu.Lock()
					key := issueKey(i)
					delete(active, key)
					jobMu.Lock()
					job := jobs[key]
					job.Status = "failed"
					job.Log += runErr.Error()
					jobs[key] = job
					jobMu.Unlock()
					work[key] = workState{Status: "failed", SessionID: work[key].SessionID}
					_ = saveScopedWorkState(w.StatePath, work, targets)
					workMu.Unlock()
					tasks.mu.Lock()
					tasks.running--
					tasks.failed++
					running, queued, completed, failed := tasks.running, tasks.queued, tasks.completed, tasks.failed
					tasks.mu.Unlock()
					w.logf("issue #%d failed: %v (tasks: %d running, %d queued, %d completed, %d failed)", i.Number, runErr, running, queued, completed, failed)
					publish()
				} else {
					if w.Status != nil {
						if statusErr := w.Status.SetIssueStatus(context.Background(), i.Target, i, "Done"); statusErr != nil {
							w.logf("issue #%d failed to update project status: %v", i.Number, statusErr)
						}
					}
					if labeler != nil && !isProjectTarget(i.Target) {
						_ = labeler.SetIssueLabel(context.Background(), issueRepository(i.Target, i), i.Number, false)
					}
					workMu.Lock()
					key := issueKey(i)
					delete(active, key)
					jobMu.Lock()
					job := jobs[key]
					job.Status = "complete"
					jobs[key] = job
					jobMu.Unlock()
					work[key] = workState{Status: "completed", SessionID: work[key].SessionID}
					_ = saveScopedWorkState(w.StatePath, work, targets)
					workMu.Unlock()
					tasks.mu.Lock()
					tasks.running--
					tasks.completed++
					running, queued, completed, failed := tasks.running, tasks.queued, tasks.completed, tasks.failed
					tasks.mu.Unlock()
					w.logf("issue #%d completed (tasks: %d running, %d queued, %d completed, %d failed)", i.Number, running, queued, completed, failed)
					publish()
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
	publish()
	var ticker *time.Ticker
	var tick <-chan time.Time
	var retryTimer *time.Timer
	var retry <-chan time.Time
	var stateReloadTimer *time.Timer
	var stateReload <-chan time.Time
	defer func() {
		if stateReloadTimer != nil {
			stateReloadTimer.Stop()
		}
	}()
	if !w.UseWebhooks {
		ticker = time.NewTicker(w.Interval)
		defer ticker.Stop()
		tick = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			w.logf("shutdown requested; waiting for running tasks to finish")
			wg.Wait()
			running, queued, completed, failed := tasks.snapshot()
			w.logf("stopped (tasks: %d running, %d queued, %d completed, %d failed)", running, queued, completed, failed)
			return nil
		case <-tick:
			if err := poll(); err != nil {
				if ctx.Err() != nil {
					w.logf("shutdown requested during poll; waiting for running tasks to finish")
					wg.Wait()
					w.logf("stopped")
					return nil
				}
				w.logf("poll #%d error: %v; will retry in %s", pollNumber, err, w.Interval)
			}
		case event := <-w.Events:
			w.logWebhookEvent(event)
			if err := poll(); err != nil {
				if ctx.Err() != nil {
					wg.Wait()
					return nil
				}
				w.logf("webhook-triggered poll #%d error: %v", pollNumber, err)
			}
			// Keep an already scheduled follow-up refresh. GitHub may deliver
			// another webhook while its issue index is still catching up; resetting
			// the timer in that case can make the refresh observe the previous
			// issue and miss the newest one until another delivery arrives.
			if retryTimer == nil {
				retryTimer = time.NewTimer(w.Interval)
				retry = retryTimer.C
			}
		case <-retry:
			retryTimer = nil
			w.logf("webhook follow-up refresh started")
			if err := poll(); err != nil && ctx.Err() == nil {
				w.logf("webhook follow-up poll #%d error: %v", pollNumber, err)
			}
			retry = nil
		case <-stateChanges:
			if stateReloadTimer == nil {
				stateReloadTimer = time.NewTimer(stateReloadDebounce)
				stateReload = stateReloadTimer.C
			} else {
				if !stateReloadTimer.Stop() {
					select {
					case <-stateReloadTimer.C:
					default:
					}
				}
				stateReloadTimer.Reset(stateReloadDebounce)
			}
		case <-stateReload:
			stateReloadTimer = nil
			stateReload = nil
			reloaded, loadErr := loadScopedWorkState(w.StatePath, targets)
			if loadErr != nil {
				w.logf("state reload failed: %v", loadErr)
				continue
			}
			workMu.Lock()
			for key, session := range active {
				reloaded[key] = workState{Status: "active", SessionID: session}
			}
			work = reloaded
			seen = make(map[string]bool, len(work))
			for key := range work {
				seen[key] = true
			}
			workMu.Unlock()
			w.logf("state reloaded; scheduling resync")
			if err := poll(); err != nil && ctx.Err() == nil {
				w.logf("state reload poll #%d error: %v", pollNumber, err)
			}
		}
	}
}

func watchStateFile(ctx context.Context, path string) <-chan struct{} {
	if path == "" {
		return nil
	}
	changes := make(chan struct{}, 1)
	previous := stateFileFingerprint(path)
	go func() {
		defer close(changes)
		for {
			current := stateFileFingerprint(path)
			if current != previous {
				select {
				case changes <- struct{}{}:
				default:
				}
				previous = current
			}
			timer := time.NewTimer(stateFilePollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return changes
}

func stateFileFingerprint(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func (w *Glorp) logWebhookEvent(event WebhookEvent) {
	switch event.Kind {
	case "push":
		w.logf("webhook push received (repository: %s, ref: %s, before: %s, after: %s, commits: %d)", event.Repository, event.Ref, event.Before, event.After, event.CommitCount)
	case "issues":
		w.logf("webhook issues received (repository: %s, action: %s, issue: #%d %q)", event.Repository, event.Action, event.IssueNumber, event.IssueTitle)
	default:
		w.logf("webhook %s received", event.Kind)
	}
}

func (w *Glorp) resetFailedWork(ctx context.Context, work map[string]workState, labeler IssueLabeler) error {
	for key, state := range work {
		if state.Status != "failed" {
			continue
		}
		separator := strings.LastIndexByte(key, '#')
		if separator <= 0 {
			return fmt.Errorf("invalid failed work key %q", key)
		}
		target := key[:separator]
		number, err := strconv.Atoi(key[separator+1:])
		if err != nil {
			return fmt.Errorf("invalid failed work key %q: %w", key, err)
		}
		issue := Issue{Number: number, Target: target}
		if labeler != nil && !isProjectTarget(target) {
			if err := labeler.SetIssueLabel(ctx, issueRepository(target, issue), number, false); err != nil {
				return fmt.Errorf("reset failed issue #%d label: %w", number, err)
			}
		}
		if w.Status != nil {
			if err := w.Status.SetIssueStatus(ctx, target, issue, "Todo"); err != nil {
				if isProjectTarget(target) && errors.Is(err, errProjectIssueNotFound) {
					w.logf("reset failed issue #%d skipped because it is no longer in project", number)
					continue
				}
				return fmt.Errorf("reset failed issue #%d project status: %w", number, err)
			}
		}
		w.logf("reset failed issue #%d to Todo", number)
	}
	return nil
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
	if wasActive {
		return true
	}
	if isProjectTarget(repo) {
		if !seen {
			return projectStatusAllowsDispatch(issue.ProjectStatus)
		}
		return issue.ProjectStatus == "In Progress"
	}
	if !seen {
		return true
	}
	return hasLabel(issue, agentStartedLabel)
}

func projectStatusAllowsDispatch(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in progress", "done", "completed":
		return false
	default:
		return true
	}
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
			issue.ProjectItemID = item.ID
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

func loadScopedWorkState(path string, targets []string) (map[string]workState, error) {
	result := make(map[string]workState)
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
		var state workState
		if json.Unmarshal(value, &state) != nil {
			var legacy bool
			if err := json.Unmarshal(value, &legacy); err != nil {
				return nil, fmt.Errorf("decode state issue %q: %w", key, err)
			}
			if legacy {
				state = workState{Status: "completed"}
			} else {
				continue
			}
		}
		if _, err := strconv.Atoi(key); err == nil {
			if len(targets) > 0 {
				result[targets[0]+"#"+key] = state
			}
		} else {
			result[key] = state
		}
	}
	return result, nil
}

func saveScopedWorkState(path string, state map[string]workState, targets []string) error {
	if path == "" {
		return nil
	}
	var value interface{} = state
	if len(targets) == 1 {
		legacy := make(map[int]workState, len(state))
		prefix := targets[0] + "#"
		for key, work := range state {
			if strings.HasPrefix(key, prefix) {
				number, err := strconv.Atoi(strings.TrimPrefix(key, prefix))
				if err == nil {
					legacy[number] = work
					continue
				}
			}
			return fmt.Errorf("invalid scoped state key %q", key)
		}
		value = legacy
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
