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
	pushFallbackInterval  = 15 * time.Minute
	webhookRetryLimit     = 3
	workClosureInterval   = 10 * time.Second
	projectProbeInterval  = 30 * time.Second
	// reapPollInterval is the longest an instance goes without a reap pass
	// over abandoned work. Polling can be much less frequent than this in
	// webhook push mode, so reaping gets its own floor (issue #239).
	reapPollInterval = 10 * time.Minute
)

var errWorkClosedByUser = errors.New("work closed by user")

// errWorkClaimedByOther signals that another glorp instance posted a newer
// starting/continuing claim while this instance was actively working the
// same issue. Per the handoff protocol (issue #214), the most recent claim
// always wins, so the losing instance cooperatively stops.
var errWorkClaimedByOther = errors.New("work claimed by another instance")

// isCooperativeCancellation reports whether cause is one of the run
// cancellation reasons that reflect another party taking over the work
// rather than an actual runner failure.
func isCooperativeCancellation(cause error) bool {
	return errors.Is(cause, errWorkClosedByUser) || errors.Is(cause, errWorkClaimedByOther)
}

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

// Discussion is a top-level GitHub Discussion thread that has not yet
// received any reply.
type Discussion struct {
	Number    int
	Title     string
	Body      string
	CreatedAt time.Time
}

type DiscussionSource interface {
	ListUnansweredDiscussions(context.Context, string) ([]Discussion, error)
}

type PullRequestWorkState struct {
	Number int
	State  string
	Merged bool
}

type OriginatingWorkState struct {
	IssueState   string
	PullRequests []PullRequestWorkState
}

type WorkClosureChecker interface {
	OriginatingWorkState(context.Context, string, int) (OriginatingWorkState, error)
}
type LabelEnsurer interface {
	EnsureLabels(context.Context, string) error
}

// ProjectStateSource returns a cheap fingerprint of a project board's
// dispatchable state. GitHub publishes no projects_v2 webhook for user-owned
// Projects, so board-only edits (dragging an issue onto the board, moving a
// card into the ready column) produce no delivery at all. Push mode probes
// this fingerprint on a short interval and only pays for a full poll when it
// actually changes, instead of waiting out the fallback interval.
type ProjectStateSource interface {
	ProjectState(context.Context, string) (string, error)
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
type AgentSession struct {
	ID                string
	Agent             string
	CheckoutDirectory string
	Resume            bool
}
type SessionAgentRunner interface {
	RunSession(context.Context, Issue, AgentSession, func(AgentSession)) error
}
type SessionAgentOutputRunner interface {
	RunSessionWithOutput(context.Context, Issue, AgentSession, func(AgentSession), io.Writer) error
}
type AgentIdentifier interface {
	AgentName() string
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
	ReadyState  string
	Issues      IssueSource
	// Discussions lists unanswered top-level Discussion threads for
	// Discussions-board targets. When nil, Discussions-board targets are
	// never polled.
	Discussions DiscussionSource
	Runner      AgentRunner
	Out         io.Writer
	// fallbackInterval overrides the push-mode polling fallback in tests.
	fallbackInterval time.Duration
	// closureInterval overrides active-work closure polling in tests.
	closureInterval time.Duration
	// Projects supplies the push-mode project board fingerprint. When nil,
	// board changes are only picked up by the fallback poll.
	Projects ProjectStateSource
	// probeInterval overrides the project board probe interval in tests.
	probeInterval time.Duration
	Labels        LabelEnsurer
	Status        IssueStatuser
	UI            UIReporter
	Quota         func(context.Context) map[string]string
	// Identity names this instance in cooperative handoff comments. It is
	// generated once at startup and never persisted.
	Identity Identity
	// Comments drives the cooperative handoff handshake (issue #214). When
	// nil, ownership negotiation is skipped and dispatch behaves as before.
	Comments CommentClient
	// Webhooks re-reconciles push webhooks on every periodic poll so a
	// repository that joins a project board after startup gets a webhook
	// without a restart (issue #238). Nil skips reconciliation, as in poll
	// mode where no webhooks are configured at all.
	Webhooks func(context.Context)
	// ownershipWait overrides the reap grace-period wait in tests.
	ownershipWait func(context.Context) bool
	// reapInterval overrides the periodic reap cadence in tests.
	reapInterval time.Duration
	// staleClaim overrides the age at which another instance's claim is
	// considered abandoned in tests.
	staleClaim time.Duration
	logMu      sync.Mutex
}

func (w *Glorp) periodicPollInterval() time.Duration {
	if w.UseWebhooks {
		if w.fallbackInterval > 0 {
			return w.fallbackInterval
		}
		return pushFallbackInterval
	}
	return w.Interval
}

// projectBoardProbeInterval is how often push mode checks project targets for
// board-only changes that produce no webhook delivery.
func (w *Glorp) projectBoardProbeInterval() time.Duration {
	if w.probeInterval > 0 {
		return w.probeInterval
	}
	return projectProbeInterval
}

// projectProbeTargets lists the targets whose boards need fingerprint probing.
// Only push mode needs it; poll mode already refreshes every Interval.
func (w *Glorp) projectProbeTargets(targets []string) []string {
	if !w.UseWebhooks || w.Projects == nil {
		return nil
	}
	var probed []string
	for _, target := range targets {
		if isProjectTarget(target) {
			probed = append(probed, target)
		}
	}
	return probed
}

// reapPollTick is how often a reap pass runs when ordinary polling is slower
// than reapPollInterval. It returns 0 when polling already runs at least that
// often, in which case every poll doubles as a reap pass.
func (w *Glorp) reapPollTick() time.Duration {
	interval := reapPollInterval
	if w.reapInterval > 0 {
		interval = w.reapInterval
	}
	if w.periodicPollInterval() <= interval {
		return 0
	}
	return interval
}

func (w *Glorp) staleClaimAfter() time.Duration {
	if w.staleClaim > 0 {
		return w.staleClaim
	}
	return staleClaimDuration
}

func (w *Glorp) activeWorkClosureInterval() time.Duration {
	if w.closureInterval > 0 {
		return w.closureInterval
	}
	return workClosureInterval
}

type pendingIssue struct {
	issue   Issue
	session AgentSession
	// contested marks a dispatch candidate that this instance has no local
	// record of handling as its own resumed work (a reclaim of another
	// instance's apparent work, or a stale local record), so its ownership
	// must be negotiated through the comment protocol before dispatch.
	contested bool
}

// negotiateContestedIssues runs the handoff handshake for every candidate
// issue marked contested (no local record of being this instance's own
// resumed work). Uncontested issues pass through untouched. Issues whose
// negotiation loses (or errors) are dropped from the batch but stay marked as
// seen, so a later poll retries them as contested work and renegotiates
// instead of dispatching them as if nothing had ever claimed them.
//
// The first reap after startup is aggressive: every contested candidate is
// asked about immediately. Later reaps run on a timer (issue #239), so they
// first check how old the newest claim from another instance is and stand
// down silently on anything claimed within staleClaimDuration, rather than
// re-posting "does anyone have this?" on every pass.
func (w *Glorp) negotiateContestedIssues(ctx context.Context, checker WorkClosureChecker, newIssues []pendingIssue, seen map[string]bool, aggressive bool) []pendingIssue {
	if w.Comments == nil || len(newIssues) == 0 {
		return newIssues
	}
	keep := make([]bool, len(newIssues))
	var wg sync.WaitGroup
	for i, pending := range newIssues {
		if pending.session.Resume || !pending.contested {
			keep[i] = true
			continue
		}
		wg.Add(1)
		go func(i int, issue Issue) {
			defer wg.Done()
			target := ownershipTargetFor(ctx, checker, issue)
			if !aggressive {
				fresh, err := w.claimIsFresh(ctx, target)
				if err != nil {
					w.logf("issue #%d reap check failed: %v", issue.Number, err)
					return
				}
				if fresh {
					w.logf("issue #%d claimed by another instance within %s; skipping reap", issue.Number, w.staleClaimAfter())
					return
				}
			}
			claimed, err := w.negotiateOwnership(ctx, target)
			if err != nil {
				w.logf("issue #%d ownership handoff failed: %v", issue.Number, err)
				return
			}
			keep[i] = claimed
			if !claimed {
				w.logf("issue #%d ownership claimed by another instance; standing down", issue.Number)
			}
		}(i, pending.issue)
	}
	wg.Wait()
	filtered := newIssues[:0]
	for i, pending := range newIssues {
		if keep[i] {
			filtered = append(filtered, pending)
		} else {
			// Keep the seen marker: it is what makes the next poll treat this
			// issue as contested again. Clearing it would make the issue look
			// brand new, and the next poll would dispatch it outright, taking
			// work from the instance this one just stood down for.
			seen[issueKey(pending.issue)] = true
		}
	}
	return filtered
}

// pollDiscussions dispatches the gh-discuss skill for newly discovered
// unanswered top-level Discussion threads on any Discussions-board targets.
// Discussion dispatch is intentionally isolated from issue dispatch: it does
// not use the persisted work state, project status, or comment-based
// handoff protocol, since GitHub's own reply count on a discussion already
// tells the next poll whether it still needs an answer. active only guards
// against dispatching the same discussion twice while an agent run for it
// is still in flight.
func (w *Glorp) pollDiscussions(ctx context.Context, n int, targets []string, sem chan struct{}, wg *sync.WaitGroup, workMu *sync.Mutex, active map[string]string) {
	if w.Discussions == nil {
		return
	}
	for _, target := range targets {
		if !isDiscussionTarget(target) {
			continue
		}
		discussions, err := w.Discussions.ListUnansweredDiscussions(ctx, target)
		if err != nil {
			w.logf("poll #%d failed while listing discussions for %s: %v", n, target, err)
			continue
		}
		for _, discussion := range discussions {
			key := target + "#discussion#" + strconv.Itoa(discussion.Number)
			workMu.Lock()
			_, inFlight := active[key]
			if !inFlight {
				active[key] = "discussion"
			}
			workMu.Unlock()
			if inFlight {
				continue
			}
			issue := Issue{Number: discussion.Number, Title: discussion.Title, Body: discussion.Body, CreatedAt: discussion.CreatedAt, Target: target, Repository: issueRepository(target, Issue{})}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				workMu.Lock()
				delete(active, key)
				workMu.Unlock()
				return
			}
			w.logf("discussion #%d queued", discussion.Number)
			wg.Add(1)
			go func(i Issue, key string) {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					workMu.Lock()
					delete(active, key)
					workMu.Unlock()
				}()
				w.logf("discussion #%d started", i.Number)
				if err := w.Runner.Run(ctx, i); err != nil {
					w.logf("discussion #%d failed: %v", i.Number, err)
					return
				}
				w.logf("discussion #%d completed", i.Number)
			}(issue, key)
		}
	}
}

func (w *Glorp) watchForClosedWork(ctx context.Context, checker WorkClosureChecker, issue Issue, cancel context.CancelCauseFunc, ready chan<- struct{}) {
	repo := issueRepository(issue.Target, issue)
	previous, err := checker.OriginatingWorkState(ctx, repo, issue.Number)
	if err != nil && ctx.Err() == nil {
		w.logf("issue #%d initial closure check failed: %v", issue.Number, err)
	}
	close(ready)
	if reason := closedWorkReason(OriginatingWorkState{}, previous, issue.Number); err == nil && strings.EqualFold(previous.IssueState, "closed") && reason != "" {
		cause := fmt.Errorf("%w: %s", errWorkClosedByUser, reason)
		w.logf("issue #%d stopping agent: %s", issue.Number, reason)
		cancel(cause)
		return
	}
	ticker := time.NewTicker(w.activeWorkClosureInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := checker.OriginatingWorkState(ctx, repo, issue.Number)
			if err != nil {
				if ctx.Err() == nil {
					w.logf("issue #%d closure check failed: %v", issue.Number, err)
				}
				continue
			}
			if reason := closedWorkReason(previous, current, issue.Number); reason != "" {
				cause := fmt.Errorf("%w: %s", errWorkClosedByUser, reason)
				w.logf("issue #%d stopping agent: %s", issue.Number, reason)
				cancel(cause)
				return
			}
			previous = current
		}
	}
}

func closedWorkReason(previous, current OriginatingWorkState, issueNumber int) string {
	if strings.EqualFold(current.IssueState, "closed") && !strings.EqualFold(previous.IssueState, "closed") {
		for _, pullRequest := range current.PullRequests {
			if pullRequest.Merged {
				return ""
			}
		}
		return fmt.Sprintf("issue #%d was closed without a merge", issueNumber)
	}
	previousPullRequests := make(map[int]PullRequestWorkState, len(previous.PullRequests))
	for _, pullRequest := range previous.PullRequests {
		previousPullRequests[pullRequest.Number] = pullRequest
	}
	for _, pullRequest := range current.PullRequests {
		old, existed := previousPullRequests[pullRequest.Number]
		if !pullRequest.Merged && strings.EqualFold(pullRequest.State, "closed") && (!existed || !strings.EqualFold(old.State, "closed")) {
			return fmt.Sprintf("pull request #%d was closed without merging", pullRequest.Number)
		}
	}
	return ""
}

// watchForCompetingClaim periodically polls comments on the negotiated
// ownership target (the issue, or an open pull request already linked to
// it) while an agent is actively working, looking for a newer
// starting/continuing claim signed by a different instance identity. Per
// the handoff protocol described in issue #214, the last instance to post
// such a claim wins, so detecting one here means this instance lost the
// ticket mid-run and must cooperatively cancel.
func (w *Glorp) watchForCompetingClaim(ctx context.Context, target ownershipTarget, issueNumber int, since time.Time, cancel context.CancelCauseFunc) {
	if w.Comments == nil {
		return
	}
	ticker := time.NewTicker(w.activeWorkClosureInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			comments, err := w.Comments.ListComments(ctx, target.Repo, target.Number)
			if err != nil {
				if ctx.Err() == nil {
					w.logf("issue #%d competing claim check failed: %v", issueNumber, err)
				}
				continue
			}
			if claimedByOther(comments, since, w.Identity) {
				cause := fmt.Errorf("%w: issue #%d claimed by another instance", errWorkClaimedByOther, issueNumber)
				w.logf("issue #%d stopping agent: claimed by another instance", issueNumber)
				cancel(cause)
				return
			}
		}
	}
}

type workState struct {
	Status            string `json:"status"`
	SessionID         string `json:"sessionId,omitempty"`
	Agent             string `json:"agent,omitempty"`
	CheckoutDirectory string `json:"checkoutDirectory,omitempty"`
	// Owner is the identity of the glorp instance that most recently claimed
	// this ticket through the handoff protocol, cached to help future reaps
	// recognize their own prior work.
	Owner string `json:"owner,omitempty"`
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
			if isDiscussionTarget(target) {
				continue
			}
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
	closureChecker, _ := w.Issues.(WorkClosureChecker)
	if err := w.resetFailedWork(context.Background(), work); err != nil {
		return err
	}
	seen := make(map[string]bool, len(work))
	restored := make(map[string]bool, len(work))
	for key := range work {
		seen[key] = true
		restored[key] = true
	}
	w.logf("watching %s (poll every %s, concurrency %d; %d handled issue(s) loaded)", strings.Join(targets, ", "), w.Interval, w.Concurrency, len(seen))
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	var tasks taskState
	var workMu sync.Mutex
	active := make(map[string]string)
	jobs := make(map[string]JobSnapshot)
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
		var quotas map[string]string
		if w.Quota != nil {
			quotas = w.Quota(ctx)
		}
		w.UI.Snapshot(GlorpSnapshot{Targets: targets, IssueCounts: counts, Running: running, Queued: queued, Completed: completed, Failed: failed, Concurrency: w.Concurrency, Interval: w.Interval, UseWebhooks: w.UseWebhooks, WebhookOnline: w.UseWebhooks, Quotas: quotas, Jobs: list})
	}
	pollNumber := 0
	// observed records the "repo#number" keys returned by the most recent
	// poll. Webhook follow-up refreshes exist only to outlast GitHub's issue
	// index lag (issue #176), so once the delivered issue shows up here the
	// remaining refreshes have nothing left to catch. Only the run loop's own
	// goroutine reads or writes it.
	var observed map[string]bool
	poll := func() error {
		pollNumber++
		n := pollNumber
		running, queued, completed, failed := tasks.snapshot()
		w.logf("poll #%d started (tasks: %d running, %d queued, %d completed, %d failed)", n, running, queued, completed, failed)
		issues := make([]Issue, 0)
		for _, target := range targets {
			if isDiscussionTarget(target) {
				continue
			}
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
		w.pollDiscussions(ctx, n, targets, sem, &wg, &workMu, active)
		observed = make(map[string]bool, len(issues))
		for _, issue := range issues {
			if issue.Repository != "" && issue.Number > 0 {
				observed[issue.Repository+"#"+strconv.Itoa(issue.Number)] = true
			}
		}
		newIssues := make([]pendingIssue, 0)
		for _, issue := range issues {
			if blocked, reason := issueBlocked(issue); blocked {
				w.logf("issue #%d not picked up: %s", issue.Number, reason)
				continue
			}
			key := issueKey(issue)
			workMu.Lock()
			state := work[key]
			// hadLocalRecord captures, before any reset below, whether this
			// instance already had a record of this issue (persisted from a
			// prior run or seen earlier this run). Its absence means the
			// issue was picked up fresh, so no other instance can plausibly
			// believe it already owns it.
			hadLocalRecord := restored[key] || seen[key]
			reconcileCompleted := isProjectTarget(issue.Target) && state.Status == "completed"
			staleRestoredState := (restored[key] || reconcileCompleted) && !workStateMatchesRemote(issue.Target, issue, state)
			if staleRestoredState {
				staleStatus := state.Status
				delete(work, key)
				delete(seen, key)
				delete(restored, key)
				state = work[key]
				w.logf("issue #%d reset stale local %s state", issue.Number, staleStatus)
			}
			_, isActive := active[key]
			wasActive := work[key].Status == "active"
			wasFailed := work[key].Status == "failed"
			wasCompleted := work[key].Status == "completed"
			workMu.Unlock()
			if issue.Number > 0 && (wasFailed || (staleRestoredState && remoteIssueAllowsDispatch(issue.Target, issue, w.ReadyState)) || shouldDispatchIssue(issue.Target, issue, isActive, wasActive, wasCompleted, seen[key], w.ReadyState)) {
				seen[key] = true
				delete(restored, key)
				// A known local failure or an active resume is unambiguously
				// this instance's own work; anything else reappearing after
				// having a local record is a reclaim that must be negotiated
				// through the comment protocol before dispatch. A project item
				// sitting at "In Progress" that this instance has no record of
				// is another instance's apparent work (typically stranded by
				// one that died mid-run), so it must be negotiated too.
				contested := (hadLocalRecord && !wasFailed && !wasActive) || (!hadLocalRecord && projectItemInProgress(issue.Target, issue))
				newIssues = append(newIssues, pendingIssue{issue: issue, contested: contested, session: AgentSession{
					ID: state.SessionID, Agent: state.Agent, CheckoutDirectory: state.CheckoutDirectory,
					// Persisted work is not an active worker after a daemon restart or
					// a prior failure. If it has a complete session identity, resume it
					// so the agent can recover the existing draft PR and worktree.
					Resume: state.SessionID != "" && state.Agent != "",
				}})
			}
		}
		newIssues = w.negotiateContestedIssues(ctx, closureChecker, newIssues, seen, n == 1)
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
		issuesToLog := make([]Issue, len(newIssues))
		for i := range newIssues {
			issuesToLog[i] = newIssues[i].issue
		}
		w.logf("poll #%d discovered %d new issue(s): %s", n, len(newIssues), issueNumbers(issuesToLog))
		for _, pending := range newIssues {
			issue := pending.issue
			session := pending.session
			if !session.Resume {
				session.Agent = ""
				if identified, ok := w.Runner.(AgentIdentifier); ok {
					session.Agent = identified.AgentName()
				}
				// Claude accepts a caller-provided session ID. Other runners retain
				// the historical generated ID unless they replace it after launch.
				if agentProvider(session.Agent) != "codex" {
					session.ID, err = newSessionID()
					if err != nil {
						return err
					}
				}
			}
			if w.Status != nil {
				if err := w.Status.SetIssueStatus(ctx, issue.Target, issue, "In Progress"); err != nil {
					w.logf("issue #%d not dispatched; failed to set project status: %v", issue.Number, err)
					continue
				}
			}
			// Contested issues already posted their starting/continuing claim
			// during negotiation. Uncontested first-time pickups still need to
			// announce ownership so other instances know it's spoken for.
			if w.Comments != nil && !session.Resume && !pending.contested {
				repo := issueRepository(issue.Target, issue)
				if err := w.Comments.PostComment(ctx, repo, issue.Number, claimComment(w.Identity, false)); err != nil {
					w.logf("issue #%d failed to post ownership claim: %v", issue.Number, err)
				}
			}
			workMu.Lock()
			key := issueKey(issue)
			active[key] = session.ID
			jobMu.Lock()
			jobs[key] = JobSnapshot{Number: issue.Number, Title: issue.Title, Status: "queued", CheckoutDirectory: session.CheckoutDirectory, SessionID: session.ID, Started: time.Now()}
			jobMu.Unlock()
			work[key] = workState{Status: "active", SessionID: session.ID, Agent: session.Agent, CheckoutDirectory: session.CheckoutDirectory, Owner: string(w.Identity)}
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
			go func(i Issue, agentSession AgentSession, running, queued int) {
				defer wg.Done()
				defer func() { <-sem }()
				runCtx, cancelRun := context.WithCancelCause(ctx)
				defer cancelRun(nil)
				var closureReady <-chan struct{}
				if closureChecker != nil {
					ready := make(chan struct{})
					closureReady = ready
					go w.watchForClosedWork(runCtx, closureChecker, i, cancelRun, ready)
				}
				if closureReady != nil {
					select {
					case <-closureReady:
					case <-runCtx.Done():
					}
				}
				if w.Comments != nil {
					target := ownershipTargetFor(runCtx, closureChecker, i)
					go w.watchForCompetingClaim(runCtx, target, i.Number, time.Now(), cancelRun)
				}
				w.logf("issue #%d started (tasks: %d running, %d queued)", i.Number, running, queued)
				jobOutput := jobOutputWriter{write: func(text string) {
					jobMu.Lock()
					job := jobs[issueKey(i)]
					job.Log += text
					jobs[issueKey(i)] = job
					jobMu.Unlock()
					publish()
				}}
				updateSession := func(update AgentSession) {
					if update.ID == "" && update.CheckoutDirectory == "" {
						return
					}
					workMu.Lock()
					key := issueKey(i)
					state := work[key]
					if update.ID != "" {
						state.SessionID = update.ID
						active[key] = update.ID
					}
					if update.CheckoutDirectory != "" {
						state.CheckoutDirectory = update.CheckoutDirectory
					}
					work[key] = state
					saveErr := saveScopedWorkState(w.StatePath, work, targets)
					workMu.Unlock()
					if saveErr != nil {
						w.logf("issue #%d failed to save agent session: %v", i.Number, saveErr)
					}
					jobMu.Lock()
					job := jobs[key]
					if update.ID != "" {
						job.SessionID = update.ID
					}
					if update.CheckoutDirectory != "" {
						job.CheckoutDirectory = update.CheckoutDirectory
					}
					jobs[key] = job
					jobMu.Unlock()
					publish()
				}
				var runErr error
				if cause := context.Cause(runCtx); isCooperativeCancellation(cause) {
					runErr = cause
				} else if w.UI != nil {
					if runner, ok := w.Runner.(SessionAgentOutputRunner); ok {
						runErr = runner.RunSessionWithOutput(runCtx, i, agentSession, updateSession, jobOutput)
					} else if runner, ok := w.Runner.(AgentOutputRunner); ok {
						runErr = runner.RunWithOutput(runCtx, i, jobOutput)
					} else {
						runErr = w.Runner.Run(runCtx, i)
					}
				} else if runner, ok := w.Runner.(SessionAgentRunner); ok {
					runErr = runner.RunSession(runCtx, i, agentSession, updateSession)
				} else {
					runErr = w.Runner.Run(runCtx, i)
				}
				if cause := context.Cause(runCtx); isCooperativeCancellation(cause) {
					runErr = cause
				}
				if runErr != nil {
					if w.Status != nil {
						if statusErr := w.Status.SetIssueStatus(context.Background(), i.Target, i, projectReadyState(w.ReadyState, i.ProjectStatus)); statusErr != nil {
							w.logf("issue #%d failed to reset project status: %v", i.Number, statusErr)
						}
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
					state := work[key]
					state.Status = "failed"
					work[key] = state
					_ = saveScopedWorkState(w.StatePath, work, targets)
					workMu.Unlock()
					if errors.Is(runErr, errWorkClaimedByOther) && state.CheckoutDirectory != "" {
						if rmErr := os.RemoveAll(state.CheckoutDirectory); rmErr != nil {
							w.logf("issue #%d failed to remove checkout directory %s: %v", i.Number, state.CheckoutDirectory, rmErr)
						} else {
							w.logf("issue #%d removed checkout directory %s after losing ownership", i.Number, state.CheckoutDirectory)
						}
					}
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
					workMu.Lock()
					key := issueKey(i)
					delete(active, key)
					jobMu.Lock()
					job := jobs[key]
					job.Status = "complete"
					jobs[key] = job
					jobMu.Unlock()
					state := work[key]
					state.Status = "completed"
					work[key] = state
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
			}(issue, session, startedRunning, startedQueued)
		}
		running, queued, _, _ = tasks.snapshot()
		w.logf("poll #%d complete; dispatched %d issue(s) (tasks: %d running, %d queued)", n, len(newIssues), running, queued)
		return nil
	}
	// respondToOwnershipAsk answers a "Does anyone have this?" handoff comment
	// (issue #214) the moment its webhook delivery arrives, rather than
	// waiting for the next poll to notice the issue is still active locally.
	respondToOwnershipAsk := func(ctx context.Context, event WebhookEvent) {
		if event.Kind != "issue_comment" || event.Action != "created" || w.Comments == nil {
			return
		}
		kind, id, ok := parseClaim(event.CommentBody)
		if !ok || kind != claimAsking || id == w.Identity {
			return
		}
		key := event.Repository + "#" + strconv.Itoa(event.IssueNumber)
		workMu.Lock()
		_, owned := active[key]
		workMu.Unlock()
		if !owned {
			return
		}
		if err := w.Comments.PostComment(ctx, event.Repository, event.IssueNumber, signComment(presenceClaimBody, w.Identity)); err != nil {
			w.logf("issue #%d failed to respond to ownership ask: %v", event.IssueNumber, err)
		}
	}
	if err := poll(); err != nil {
		if ctx.Err() != nil {
			wg.Wait()
			w.logf("stopped during initial poll")
			return nil
		}
		w.logf("initial poll error: %v; will retry in %s", err, w.periodicPollInterval())
	}
	publish()
	// Board-only project changes never reach a webhook, so push mode probes a
	// cheap board fingerprint instead. Seeding it here (rather than on the
	// first tick) means an idle board costs one small request per probe and
	// never triggers a redundant full poll at startup.
	probedTargets := w.projectProbeTargets(targets)
	boardState := make(map[string]string, len(probedTargets))
	probeBoards := func(ctx context.Context) bool {
		changed := false
		for _, target := range probedTargets {
			state, err := w.Projects.ProjectState(ctx, target)
			if err != nil {
				if ctx.Err() == nil {
					w.logf("project board probe for %s failed: %v", target, err)
				}
				continue
			}
			previous, known := boardState[target]
			boardState[target] = state
			if known && previous != state {
				w.logf("project board change detected for %s; refreshing", target)
				changed = true
			}
		}
		return changed
	}
	probeBoards(ctx)
	var ticker *time.Ticker
	var tick <-chan time.Time
	var retryTimer *time.Timer
	var retry <-chan time.Time
	retriesRemaining := 0
	// pendingWebhookIssue names the issue whose delivery scheduled the current
	// follow-up chain, so the chain can stop early once a refresh sees it.
	pendingWebhookIssue := ""
	var stateReloadTimer *time.Timer
	var stateReload <-chan time.Time
	defer func() {
		if stateReloadTimer != nil {
			stateReloadTimer.Stop()
		}
	}()
	// Keep periodic reconciliation active in webhook mode so issue state that
	// changes without a delivery can still be recovered.
	periodicInterval := w.periodicPollInterval()
	ticker = time.NewTicker(periodicInterval)
	defer ticker.Stop()
	tick = ticker.C
	var boardTick <-chan time.Time
	if len(probedTargets) > 0 {
		boardTicker := time.NewTicker(w.projectBoardProbeInterval())
		defer boardTicker.Stop()
		boardTick = boardTicker.C
		w.logf("probing %d project board(s) for board-only changes every %s", len(probedTargets), w.projectBoardProbeInterval())
	}
	// Reaping abandoned work rides along with polling, so when polling is
	// slower than reapPollInterval (webhook push mode) it gets its own,
	// faster ticker (issue #239).
	var reap <-chan time.Time
	if reapTick := w.reapPollTick(); reapTick > 0 {
		reapTicker := time.NewTicker(reapTick)
		defer reapTicker.Stop()
		reap = reapTicker.C
		w.logf("reaping abandoned work every %s", reapTick)
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
			if w.Webhooks != nil {
				w.Webhooks(ctx)
			}
			if err := poll(); err != nil {
				if ctx.Err() != nil {
					w.logf("shutdown requested during poll; waiting for running tasks to finish")
					wg.Wait()
					w.logf("stopped")
					return nil
				}
				w.logf("poll #%d error: %v; will retry in %s", pollNumber, err, periodicInterval)
			}
		case <-boardTick:
			if !probeBoards(ctx) {
				continue
			}
			if err := poll(); err != nil && ctx.Err() == nil {
				w.logf("project board poll #%d error: %v", pollNumber, err)
			}
		case <-reap:
			w.logf("periodic reap started")
			if err := poll(); err != nil && ctx.Err() == nil {
				w.logf("reap poll #%d error: %v", pollNumber, err)
			}
		case event := <-w.Events:
			w.logWebhookEvent(event)
			respondToOwnershipAsk(ctx, event)
			// The payload alone decides whether this delivery could have
			// changed dispatchable work. Pushes, pull request activity, and
			// ordinary comments never do, so they no longer cost a refresh.
			if !webhookEventNeedsRefresh(event) {
				w.logf("webhook %s delivery cannot change issue state; skipping refresh", webhookEventLabel(event))
				continue
			}
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
				if key, ok := webhookIssueKey(event); ok && observed[key] {
					// The refresh above already saw the delivered issue, so
					// there is no index lag left for follow-ups to outlast.
					continue
				}
				pendingWebhookIssue, _ = webhookIssueKey(event)
				retryTimer = time.NewTimer(w.Interval)
				retry = retryTimer.C
				retriesRemaining = webhookRetryLimit
			}
		case <-retry:
			retryTimer = nil
			w.logf("webhook follow-up refresh started")
			if err := poll(); err != nil && ctx.Err() == nil {
				w.logf("webhook follow-up poll #%d error: %v", pollNumber, err)
			}
			retriesRemaining--
			if pendingWebhookIssue != "" && observed[pendingWebhookIssue] {
				w.logf("webhook follow-up refreshes complete; issue %s observed", pendingWebhookIssue)
				retriesRemaining = 0
			}
			if retriesRemaining > 0 {
				retryTimer = time.NewTimer(w.Interval)
				retry = retryTimer.C
			} else {
				pendingWebhookIssue = ""
				retry = nil
			}
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
				state := work[key]
				state.Status = "active"
				state.SessionID = session
				reloaded[key] = state
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

// webhookEventNeedsRefresh reports whether a delivery could have changed which
// issues are dispatchable, using only the payload glorp already decoded. Push,
// pull request, ping, and comment deliveries never do: closing an issue from a
// merged pull request arrives separately as an `issues` closed event, and the
// handoff handshake answers comments directly without a refresh. Unrecognized
// event kinds refresh so a newly subscribed event is never silently dropped.
func webhookEventNeedsRefresh(event WebhookEvent) bool {
	switch event.Kind {
	case "push", "ping", "pull_request", "issue_comment":
		return false
	case "issues":
		switch event.Action {
		case "opened", "reopened", "closed", "deleted", "transferred", "labeled", "unlabeled":
			return true
		default:
			// edited, assigned, milestoned, locked, pinned, and friends leave
			// every dispatch input (labels, state, dependencies) untouched.
			return false
		}
	case "projects_v2_item":
		// Only reordering leaves an item's status untouched; every other
		// action can move it into or out of the ready state.
		return event.Action != "reordered"
	case "discussion":
		switch event.Action {
		case "created", "reopened", "transferred":
			return true
		default:
			// A discussion is dispatchable purely on existing with no reply
			// yet, so edits, labels, pins, and answer changes cannot make a
			// thread newly answerable.
			return false
		}
	default:
		return true
	}
}

// webhookIssueKey returns the "repo#number" key the delivery refers to, when it
// names one.
func webhookIssueKey(event WebhookEvent) (string, bool) {
	if event.Repository == "" || event.IssueNumber <= 0 {
		return "", false
	}
	return event.Repository + "#" + strconv.Itoa(event.IssueNumber), true
}

// webhookEventLabel names a delivery for logging, including its action when the
// payload carried one.
func webhookEventLabel(event WebhookEvent) string {
	if event.Action == "" {
		return event.Kind
	}
	return event.Kind + "/" + event.Action
}

func (w *Glorp) logWebhookEvent(event WebhookEvent) {
	switch event.Kind {
	case "push":
		w.logf("webhook push received (repository: %s, ref: %s, before: %s, after: %s, commits: %d)", event.Repository, event.Ref, event.Before, event.After, event.CommitCount)
	case "issues":
		w.logf("webhook issues received (repository: %s, action: %s, issue: #%d %q)", event.Repository, event.Action, event.IssueNumber, event.IssueTitle)
	case "projects_v2_item":
		w.logf("webhook project item received (action: %s)", event.Action)
	case "issue_comment":
		w.logf("webhook issue comment received (repository: %s, action: %s, issue: #%d)", event.Repository, event.Action, event.IssueNumber)
	case "discussion":
		w.logf("webhook discussion received (repository: %s, action: %s, discussion: #%d %q)", event.Repository, event.Action, event.DiscussionNumber, event.DiscussionTitle)
	default:
		w.logf("webhook %s received", event.Kind)
	}
}

func (w *Glorp) resetFailedWork(ctx context.Context, work map[string]workState) error {
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
		if w.Status != nil {
			readyState := projectReadyState(w.ReadyState, "")
			if err := w.Status.SetIssueStatus(ctx, target, issue, readyState); err != nil {
				if isProjectTarget(target) && errors.Is(err, errProjectIssueNotFound) {
					w.logf("reset failed issue #%d skipped because it is no longer in project", number)
					continue
				}
				w.logf("reset failed issue #%d project status: %v", number, err)
				continue
			}
			w.logf("reset failed issue #%d to %s", number, readyState)
			continue
		}
		w.logf("reset failed issue #%d", number)
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

// shouldDispatchIssue decides whether a repository or project issue that is
// not already active locally is a dispatch candidate. Remote ownership can
// no longer be read synchronously for repository issues (no label survives
// to check), so a never-seen issue is always a candidate, and one this
// instance has already seen is a candidate again only if it wasn't already
// completed; negotiateContestedIssues is what asks, via comments, whether
// another instance already owns a reappearing candidate.
func shouldDispatchIssue(repo string, issue Issue, isActive, wasActive, wasCompleted, seen bool, readyState string) bool {
	if isActive {
		return false
	}
	if wasActive {
		return true
	}
	if isProjectTarget(repo) {
		if projectStatusAllowsDispatch(issue.ProjectStatus, readyState) {
			return true
		}
		// An item parked at "In Progress" is claimed work: either this
		// instance's own reappearing item, or work stranded in that column by
		// an instance that died mid-run and left this one no local record to
		// recognize it by. Both are dispatch candidates;
		// negotiateContestedIssues is what asks, through the comment protocol,
		// whether another live instance still owns it before it is reclaimed.
		return projectItemInProgress(repo, issue)
	}
	if !seen {
		return true
	}
	return !wasCompleted
}

// projectItemInProgress reports whether issue is a project item currently
// parked in the "In Progress" column, which is how a glorp instance marks
// work it has claimed.
func projectItemInProgress(target string, issue Issue) bool {
	return isProjectTarget(target) && strings.EqualFold(strings.TrimSpace(issue.ProjectStatus), "In Progress")
}

func workStateMatchesRemote(target string, issue Issue, state workState) bool {
	switch state.Status {
	case "active":
		if isProjectTarget(target) {
			return strings.EqualFold(strings.TrimSpace(issue.ProjectStatus), "In Progress")
		}
		// Repository work state persists only in this instance's own
		// .glorp.json, and there is no remote label left to cross-check.
		// Trust it only when it carries enough identity to actually resume
		// (a session ID and agent); an incomplete record can't be resumed
		// reliably, so treat it as stale and let a fresh pickup negotiate
		// ownership through the comment protocol instead.
		return state.SessionID != "" && state.Agent != ""
	case "completed":
		if !isProjectTarget(target) {
			// Repository issue queries only return open issues, so an issue present
			// in the batch cannot still be completed remotely.
			return false
		}
		status := strings.TrimSpace(issue.ProjectStatus)
		return strings.EqualFold(status, "Done") || strings.EqualFold(status, "Completed")
	default:
		return true
	}
}

func remoteIssueAllowsDispatch(target string, issue Issue, readyState string) bool {
	if !isProjectTarget(target) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(issue.ProjectStatus), "In Progress") ||
		projectStatusAllowsDispatch(issue.ProjectStatus, readyState)
}

func projectStatusAllowsDispatch(status, readyState string) bool {
	status = strings.TrimSpace(status)
	readyState = strings.TrimSpace(readyState)
	if readyState != "" {
		return strings.EqualFold(status, readyState)
	}
	return strings.EqualFold(status, "Todo") || strings.EqualFold(status, "Ready")
}

func projectReadyState(configured, current string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	if current = strings.TrimSpace(current); projectStatusAllowsDispatch(current, "") {
		return current
	}
	return "Todo"
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
	return issuesFromProjectItems(items), nil
}

func issuesFromProjectItems(items []projectItem) []Issue {
	issues := make([]Issue, 0, len(items))
	for _, item := range items {
		if item.Content != nil && item.Content.Type == "Issue" {
			issue := item.Content.Issue
			issue.ProjectStatus = item.Status
			issue.ProjectItemID = item.ID
			issues = append(issues, issue)
		}
	}
	return issues
}

func isProjectTarget(repo string) bool {
	target, err := parseTarget(repo)
	return err == nil && target.isProject
}

func isDiscussionTarget(repo string) bool {
	target, err := parseTarget(repo)
	return err == nil && target.isDiscussion
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
		if detail := strings.TrimSpace(string(data)); detail != "" {
			return nil, fmt.Errorf("list project fields: %w: %s", err, detail)
		}
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
