package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsegal/glorp/core"
)

const (
	stateFilePollInterval = 100 * time.Millisecond
	stateReloadDebounce   = 5 * time.Second
	pushFallbackInterval  = 15 * time.Minute
	webhookRetryLimit     = 3
	// workClosureInterval governs how often each actively-worked issue is
	// polled for closure/competing-claim signals. It matches the default
	// base poll interval rather than undercutting it, since every active
	// issue runs two of these loops in parallel and each poll costs several
	// GitHub API requests; a tighter interval was burning quota far faster
	// than the base issue-list poll (issue #276).
	workClosureInterval  = 30 * time.Second
	projectProbeInterval = 30 * time.Second
	// reapPollInterval is the longest an instance goes without a reap pass
	// over abandoned work. Polling can be much less frequent than this in
	// webhook push mode, so reaping gets its own floor (issue #239).
	reapPollInterval = 10 * time.Minute
)

var errWorkClosedByUser = errors.New("work closed by user")

var errWorkStoppedFromWebUI = errors.New("work stopped from web UI")

type jobAction struct {
	Action string `json:"action"`
	Target string `json:"target"`
	Number int    `json:"number"`
}

type jobActionRequest struct {
	action jobAction
	done   chan error
}

// errWorkClaimedByOther signals that another glorp instance posted a newer
// starting/continuing claim while this instance was actively working the
// same issue. Per the handoff protocol (issue #214), the most recent claim
// always wins, so the losing instance cooperatively stops.
var errWorkClaimedByOther = errors.New("work claimed by another instance")

// errWorkUpdated signals that the issue an agent is working changed on GitHub
// while the run was in flight (issue #469). Unlike the other cancellation
// causes it is not the end of the work: the run loop stops the agent's current
// prompt and immediately resumes the same session with workUpdate's
// instruction, so the closure, edited description, or new comment reaches the
// agent already holding the ticket.
var errWorkUpdated = errors.New("work updated")

// workUpdate is the errWorkUpdated cause, carrying the instruction to prompt
// the resumed session with.
type workUpdate struct {
	instruction string
	// summary is the short form logged when the run is interrupted; the
	// instruction itself is written for the agent, not for the log.
	summary string
	// closed marks the update a closure raised, so the cleanup run it starts
	// is not interrupted again by the same closure.
	closed bool
}

func (u *workUpdate) Error() string { return errWorkUpdated.Error() + ": " + u.summary }

func (u *workUpdate) Unwrap() error { return errWorkUpdated }

// workUpdateFor returns the update a run was interrupted to deliver, if it was
// interrupted for one at all.
func workUpdateFor(cause error) (*workUpdate, bool) {
	var update *workUpdate
	if errors.As(cause, &update) {
		return update, true
	}
	return nil, false
}

// isCooperativeCancellation reports whether cause is one of the run
// cancellation reasons that reflect another party taking over the work
// rather than an actual runner failure.
func isCooperativeCancellation(cause error) bool {
	return errors.Is(cause, errWorkClosedByUser) || errors.Is(cause, errWorkClaimedByOther)
}

// Issue and its parts live in package core so the browser driver can produce
// them without importing the root package.
type (
	Issue           = core.Issue
	IssueLabel      = core.IssueLabel
	IssueDependency = core.IssueDependency
	IssueSource     = core.IssueSource
)

// issueRepository resolves the OWNER/REPO an issue belongs to.
func issueRepository(target string, issue Issue) string {
	return core.IssueRepository(target, issue)
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

type (
	PullRequestWorkState = core.PullRequestWorkState
	OriginatingWorkState = core.OriginatingWorkState
	WorkClosureChecker   = core.WorkClosureChecker
)

type ProjectStateSource = core.ProjectStateSource
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
	// Update carries a change made to the issue while the agent was working
	// it (issue #469). A resumed session that has one is prompted with it
	// instead of the generic "continue", so the closure, edited description,
	// or new comment reaches the agent that is already holding the work
	// rather than being noticed only after the run ends.
	Update string
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
	Status        IssueStatuser
	UI            UIReporter
	Quota         func(context.Context) map[string]string
	// Identity names this instance in cooperative handoff comments. It is
	// generated once at startup and never persisted.
	Identity Identity
	// AllowedCommenters restricts which GitHub logins may trigger a direct
	// @/glorp:ID mention run (issue #294). An empty list places no
	// restriction; the CLI defaults it to the authenticated gh user.
	AllowedCommenters []string
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
	// handshakeMu guards handshakes, this instance's own record of the
	// ownership negotiations it has already run (issue #432).
	handshakeMu sync.Mutex
	handshakes  map[string]handshakeRecord
	// negotiateMu guards negotiating, the set of issues whose ownership
	// handshake is waiting out its grace window in the background. The wait
	// lasts minutes, so it must not run on the poll loop (issue #437); later
	// polls skip an issue that already has a handshake in flight instead of
	// posting a duplicate ask.
	negotiateMu sync.Mutex
	negotiating map[string]bool
	// negotiateWG tracks those background handshakes so a caller can wait
	// for them to drain.
	negotiateWG sync.WaitGroup
	// negotiatedOnce/negotiated nudge the Run loop to poll again as soon as a
	// background handshake wins its ticket, mirroring the
	// jobActionOnce/jobActions pattern above, so reclaimed work is dispatched
	// then rather than waiting out a slow tick or the next reap.
	negotiatedOnce sync.Once
	negotiated     chan struct{}
	logMu          sync.Mutex
	// repeatMu guards lastLogged, the state last reported under each
	// logChanged key. A poll loop ticking every few seconds must report a
	// summary, or a failure, once rather than on every tick (issue #413).
	repeatMu      sync.Mutex
	lastLogged    map[string]string
	jobActionOnce sync.Once
	jobActions    chan jobActionRequest
	// settingsOnce/settingsRequests carry live settings updates (issue #341)
	// into the Run loop, mirroring the jobActionOnce/jobActions pattern above.
	settingsOnce     sync.Once
	settingsRequests chan settingsRequest
	// agentOverride holds a live-updated primary agent spec (issue #341). It
	// is read from goroutines the Run loop spawns, so it is a pointer stored
	// atomically rather than a plain field owned by the loop's goroutine.
	agentOverride atomic.Pointer[string]
	// handledIssues snapshots the issue keys this instance already has in
	// flight or has finished, taken from .glorp.json and the active set at
	// the top of every poll. Browser mode reads it through issueHandled to
	// decide which extracted issues still need a metadata fetch (issue
	// #381); the issue source runs on the poll's own goroutine but the
	// snapshot is published under workMu, so it is stored atomically.
	handledIssues atomic.Pointer[map[string]bool]
}

// publishHandledIssues records which issues are no longer dispatch candidates:
// anything with a run in flight, and anything this instance already completed.
// Failed work is deliberately absent, because it is dispatched again and so
// still needs its metadata. Callers hold the lock guarding work and active.
func (w *Glorp) publishHandledIssues(work map[string]workState, active map[string]string) {
	handled := make(map[string]bool, len(work)+len(active))
	for key := range active {
		handled[key] = true
	}
	for key, state := range work {
		if state.Status == "active" || state.Status == "completed" {
			handled[key] = true
		}
	}
	w.handledIssues.Store(&handled)
}

// issueHandled reports whether the most recent snapshot already accounts for
// issue, so a transport that pays per issue for metadata can skip it. An
// unpublished snapshot treats every issue as unhandled, which is correct for
// the first poll: nothing is in flight yet.
func (w *Glorp) issueHandled(issue Issue) bool {
	handled := w.handledIssues.Load()
	if handled == nil {
		return false
	}
	return (*handled)[issueKey(issue)]
}

// acquireSlot blocks until sem has a free slot. Unlike sem.acquire, it also
// services live settings updates (issue #341) while waiting, since it runs
// on the same goroutine as the Run loop's settingsRequestsChan case: raising
// --concurrency is most useful exactly when every slot is taken, and a plain
// sem.acquire here would block that goroutine and deadlock the very request
// meant to free it.
func (w *Glorp) acquireSlot(ctx context.Context, sem *concurrencySemaphore) error {
	for {
		select {
		case <-sem.tokens:
			return nil
		case request := <-w.settingsRequestsChan():
			request.done <- w.applySettingsRequest(request.update, sem)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (w *Glorp) jobActionRequests() chan jobActionRequest {
	w.jobActionOnce.Do(func() { w.jobActions = make(chan jobActionRequest) })
	return w.jobActions
}

func (w *Glorp) handleJobAction(ctx context.Context, action jobAction) error {
	request := jobActionRequest{action: action, done: make(chan error, 1)}
	select {
	case w.jobActionRequests() <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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
// Only push mode needs it; poll mode already refreshes every Interval, and a
// board that pushes its own changes needs no probe at all (issue #249).
func (w *Glorp) projectProbeTargets(targets []string) []string {
	if !w.UseWebhooks || w.Projects == nil {
		return nil
	}
	var probed []string
	for _, target := range targets {
		if isProjectTarget(target) && !boardPushesChanges(target) {
			probed = append(probed, target)
		}
	}
	return probed
}

// pushedBoardTargets lists the project targets whose board changes arrive as
// webhook deliveries, so they are deliberately left unprobed.
func (w *Glorp) pushedBoardTargets(targets []string) []string {
	if !w.UseWebhooks {
		return nil
	}
	var pushed []string
	for _, target := range targets {
		if boardPushesChanges(target) {
			pushed = append(pushed, target)
		}
	}
	return pushed
}

// boardPushesChanges reports whether GitHub delivers projects_v2_item events
// for a project target. An organization-owned project gets an organization
// webhook covering every board edit (issue #138), and push mode refuses to
// start when that hook cannot be installed, so probing one is pure duplicate
// polling. User-owned projects get no such event, and a repository-scoped
// project URL does not say who owns the board, so both keep probing.
func boardPushesChanges(repo string) bool {
	target, err := parseTarget(repo)
	return err == nil && target.IsProject && target.ProjectOwnerType == "orgs"
}

// watchDescription names the refresh strategy in the startup log. Push mode
// never polls at Interval, so reporting that interval invites the question of
// why a board still looks polled (issue #249).
func (w *Glorp) watchDescription() string {
	if !w.UseWebhooks {
		return fmt.Sprintf("polling every %s", formatInterval(w.Interval))
	}
	return fmt.Sprintf("webhook push with a %s fallback poll", formatInterval(w.periodicPollInterval()))
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

// negotiatedNudges is the channel a settled background handshake signals the
// Run loop on. It is buffered so a handshake never blocks on a loop that is
// busy, and depth one because the nudge only asks for one more poll however
// many handshakes settled.
func (w *Glorp) negotiatedNudges() chan struct{} {
	w.negotiatedOnce.Do(func() { w.negotiated = make(chan struct{}, 1) })
	return w.negotiated
}

// nudgePoll asks the Run loop to poll again, dropping the nudge when one is
// already queued.
func (w *Glorp) nudgePoll() {
	select {
	case w.negotiatedNudges() <- struct{}{}:
	default:
	}
}

// beginNegotiation reserves the background ownership handshake for key,
// reporting false when one is already in flight. Polling continues while a
// handshake waits out its grace window, so every poll in that window sees the
// same issue as contested again; without this guard each one would post
// another "Does anyone have this?" on the same ticket.
func (w *Glorp) beginNegotiation(key string) bool {
	w.negotiateMu.Lock()
	defer w.negotiateMu.Unlock()
	if w.negotiating[key] {
		return false
	}
	if w.negotiating == nil {
		w.negotiating = make(map[string]bool)
	}
	w.negotiating[key] = true
	return true
}

// endNegotiation releases the reservation taken by beginNegotiation.
func (w *Glorp) endNegotiation(key string) {
	w.negotiateMu.Lock()
	defer w.negotiateMu.Unlock()
	delete(w.negotiating, key)
}

// negotiationInFlight reports whether a background handshake for key is still
// running, so the reap can leave it alone rather than re-announcing it.
func (w *Glorp) negotiationInFlight(key string) bool {
	w.negotiateMu.Lock()
	defer w.negotiateMu.Unlock()
	return w.negotiating[key]
}

// awaitNegotiations blocks until every background handshake this instance
// started has finished.
func (w *Glorp) awaitNegotiations() {
	w.negotiateWG.Wait()
}

type pendingIssue struct {
	issue   Issue
	session AgentSession
	// contested marks a dispatch candidate that this instance has no local
	// record of handling as its own resumed work (a reclaim of another
	// instance's apparent work, or a stale local record), so its ownership
	// must be negotiated through the comment protocol before dispatch.
	contested bool
	// claim names the target this instance holds an ownership claim on,
	// posted by the handoff handshake before the dispatch it announces has
	// happened. A dispatch that is skipped after that point must withdraw the
	// claim rather than leave the ticket owned by an idle instance (issue
	// #434).
	claim *ownershipTarget
}

// releasePendingClaim withdraws the ownership claim a won handshake already
// posted for a candidate this instance turns out not to dispatch. Without it
// the ticket stays claimed by an instance with no work record and no running
// agent: other instances read the claim and stand down for work nobody is
// doing (issue #434). Candidates that hold no claim of this instance's own
// have nothing to withdraw.
func (w *Glorp) releasePendingClaim(ctx context.Context, pending pendingIssue, reason string) {
	if pending.claim == nil {
		return
	}
	w.releaseOwnership(ctx, *pending.claim, reason)
}

// negotiateContestedIssues runs the handoff handshake for every candidate
// issue marked contested (no local record of being this instance's own
// resumed work). Uncontested issues pass through untouched.
//
// The handshake's grace window lasts minutes, so it runs in the background
// and its issue leaves this batch: an issue being negotiated, one whose
// negotiation loses, and one whose negotiation errors are all dropped but
// stay marked as seen, so a later poll retries them as contested work rather
// than dispatching them as if nothing had ever claimed them. A won handshake
// leaves this instance's own claim on the ticket, which is what the next poll
// reads to dispatch the work (issue #437).
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
	contested := make([]Issue, 0, len(newIssues))
	for _, pending := range newIssues {
		if pending.session.Resume || !pending.contested {
			continue
		}
		// A handshake already waiting out its grace window in the background
		// is this reap's answer for that issue; announcing it again on every
		// poll of that window would only repeat itself.
		if w.negotiationInFlight(issueKey(pending.issue)) {
			continue
		}
		contested = append(contested, pending.issue)
	}
	if len(contested) > 0 {
		mode := "periodic reap, skipping anything claimed within " + w.staleClaimAfter().String()
		if aggressive {
			mode = "first reap after startup, asking unconditionally"
		}
		w.logf("reaping %d contested issue(s) as %s (%s): %s", len(contested), w.Identity, mode, issueNumbers(contested))
	}
	var wg sync.WaitGroup
	for i, pending := range newIssues {
		if pending.session.Resume || !pending.contested {
			keep[i] = true
			continue
		}
		if w.negotiationInFlight(issueKey(pending.issue)) {
			continue
		}
		wg.Add(1)
		go func(i int, issue Issue) {
			defer wg.Done()
			target := ownershipTargetFor(ctx, checker, issue)
			reason := "it reappeared with no local record of this instance owning it"
			if projectItemInProgress(issue.Target, issue) {
				reason = "it sits at In Progress with no local record of this instance owning it"
			}
			w.logf("issue #%d looks claimed: %s; negotiating on %s", issue.Number, reason, target.describe())
			if !aggressive {
				standing, err := w.claimStanding(ctx, target)
				if err != nil {
					w.logf("issue #%d reap check failed: %v", issue.Number, err)
					return
				}
				// This instance already holds the newest claim, so the
				// handshake it would run has already been run and won. Asking
				// again would spam the ticket with an ask/claim pair on every
				// pass and stall each one for the grace period (issue #425).
				if standing.SelfHolds {
					w.logf("issue #%d already claimed by this instance %s ago; dispatching without re-asking", issue.Number, standing.SelfAge.Round(time.Second))
					keep[i] = true
					newIssues[i].claim = &target
					return
				}
				if standing.OwnerFresh {
					w.logf("issue #%d claimed by instance %s %s ago (within %s); skipping reap", issue.Number, standing.Owner, standing.OwnerAge.Round(time.Second), w.staleClaimAfter())
					return
				}
				// The checks above are all derived from the ticket's own
				// comments. This one is local: a negotiation this instance
				// already ran on this target settled who owns the work, and
				// re-running it would repost the same ask and the same claim
				// on every pass, stalling each one for the grace window. That
				// is the comment spam of issue #432, so the decision is reused
				// until the record goes stale even when the comments read as
				// if nothing had ever been negotiated.
				if record, age, ok := w.settledHandshake(target); ok {
					if record.Claimed {
						w.logf("issue #%d handshake already settled %s ago in this instance's favour; dispatching without re-asking", issue.Number, age.Round(time.Second))
						keep[i] = true
						newIssues[i].claim = &target
					} else {
						w.logf("issue #%d handshake already settled %s ago for another instance; standing down without re-asking", issue.Number, age.Round(time.Second))
					}
					return
				}
				if !standing.OwnerClaimed {
					w.logf("issue #%d has no claim from another instance; treating it as abandoned", issue.Number)
				} else {
					w.logf("issue #%d last claimed by instance %s %s ago (older than %s); treating it as abandoned", issue.Number, standing.Owner, standing.OwnerAge.Round(time.Second), w.staleClaimAfter())
				}
			}
			// The handshake waits out a grace window measured in minutes.
			// Running it here would stop the poll loop for that whole window,
			// so nothing else is read or dispatched meanwhile (issue #437).
			// It is handed to a goroutine instead, and the poll returns
			// without this issue: once the handshake settles, the next poll
			// reads its own claim (or its handshake record) and dispatches
			// without asking again.
			key := issueKey(issue)
			if !w.beginNegotiation(key) {
				return
			}
			w.negotiateWG.Add(1)
			w.logf("issue #%d negotiating ownership in the background; polling continues while it waits", issue.Number)
			go func() {
				defer w.negotiateWG.Done()
				defer w.endNegotiation(key)
				claimed, err := w.negotiateOwnership(ctx, target)
				if err != nil {
					w.logf("issue #%d ownership handoff failed: %v", issue.Number, err)
					return
				}
				w.recordHandshake(target, claimed)
				if claimed {
					w.logf("issue #%d picked up after handoff; dispatching on the next poll", issue.Number)
					w.nudgePoll()
				} else {
					w.logf("issue #%d ownership claimed by another instance; standing down", issue.Number)
				}
			}()
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
func (w *Glorp) pollDiscussions(ctx context.Context, n int, targets []string, sem *concurrencySemaphore, wg *sync.WaitGroup, workMu *sync.Mutex, active map[string]string) {
	if w.Discussions == nil {
		return
	}
	// Snapshot the in-flight counts before this poll claims anything, so the
	// rotation below is led by work that is genuinely still running rather
	// than by the candidates this poll is about to queue.
	workMu.Lock()
	counts := activeCountsByTarget(active, parseDiscussionWorkKey)
	workMu.Unlock()
	var pending []pendingIssue
	for _, target := range targets {
		if !isDiscussionTarget(target) {
			continue
		}
		discussions, err := w.Discussions.ListUnansweredDiscussions(ctx, target)
		if err != nil {
			w.logChanged("discussions:"+target, err.Error(), "poll #%d failed while listing discussions for %s: %v", n, target, err)
			continue
		}
		for _, discussion := range discussions {
			key := discussionWorkKey(target, discussion.Number)
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
			pending = append(pending, pendingIssue{issue: issue})
		}
	}
	if len(pending) == 0 {
		return
	}
	// Every target's candidates are collected before any of them blocks on a
	// slot, so one Discussions target's backlog cannot take every slot ahead
	// of another target's threads (issue #467).
	pending = balanceAcrossTargets(pending, counts)
	for index, candidate := range pending {
		issue := candidate.issue
		if err := w.acquireSlot(ctx, sem); err != nil {
			// Hand back this candidate's claim and every one still behind it;
			// they were never dispatched, so a later poll must see them as
			// unanswered again.
			workMu.Lock()
			for _, remaining := range pending[index:] {
				delete(active, discussionWorkKey(remaining.issue.Target, remaining.issue.Number))
			}
			workMu.Unlock()
			return
		}
		w.logf("discussion #%d queued", issue.Number)
		wg.Add(1)
		go func(i Issue, key string) {
			defer wg.Done()
			defer sem.release()
			defer func() {
				workMu.Lock()
				delete(active, key)
				workMu.Unlock()
			}()
			w.logf("discussion #%d started", i.Number)
			if err := w.runner().Run(ctx, i); err != nil {
				w.logf("discussion #%d failed: %v", i.Number, err)
				return
			}
			w.logf("discussion #%d completed", i.Number)
		}(issue, discussionWorkKey(issue.Target, issue.Number))
	}
}

// issueWatch describes the run watchForIssueUpdates is watching for, so a
// resumed run does not re-deliver what interrupted its predecessor.
type issueWatch struct {
	// since is when the run started; only comments posted after it are news
	// to the agent.
	since time.Time
	// resumable reports whether this run can be interrupted and resumed in
	// place. It is a function rather than a flag because an agent that names
	// its own session only reports it after launch, so a run can become
	// resumable partway through. A run that is not resumable keeps the
	// historical behaviour: a closure stops it outright rather than asking it
	// to clean up in a session that cannot be reopened, and an edit or a
	// comment leaves it alone entirely.
	resumable func() bool
	// closed reports that the issue was already closed when this run began,
	// which is true of the cleanup run a closure itself started. Without it
	// that run would be interrupted by the very closure it was resumed to
	// handle.
	closed bool
	// nudge asks for an immediate check instead of waiting out the tick, so a
	// webhook delivery reaches the running agent as promptly in push mode as
	// the poll notices it in poll mode.
	nudge <-chan struct{}
}

// canRelay reports whether this run can be resumed in place right now.
func (watch issueWatch) canRelay() bool {
	return watch.resumable != nil && watch.resumable()
}

// watchForIssueUpdates polls the issue an agent is working and interrupts the
// run when GitHub says something about it changed (issue #469). A run that can
// be resumed is interrupted with a workUpdate the run loop replays into the
// same session; one that cannot keeps the original behaviour of stopping on a
// closure. Closure and description changes are read from the closure checker,
// new comments from the comment client, so browser mode watches the rendered
// pages the rest of the run already reads and poll mode watches the API.
func (w *Glorp) watchForIssueUpdates(ctx context.Context, checker WorkClosureChecker, issue Issue, watch issueWatch, cancel context.CancelCauseFunc, ready chan<- struct{}) {
	repo := issueRepository(issue.Target, issue)
	var previous OriginatingWorkState
	var err error
	if checker != nil {
		previous, err = checker.OriginatingWorkState(ctx, repo, issue.Number)
		if err != nil && ctx.Err() == nil {
			w.logf("issue #%d initial closure check failed: %v", issue.Number, err)
		}
	}
	seen := w.commentsSeen(ctx, repo, issue.Number)
	close(ready)
	if reason := closedWorkReason(OriginatingWorkState{}, previous, issue.Number); checker != nil && err == nil && !watch.closed && strings.EqualFold(previous.IssueState, "closed") && reason != "" {
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
		case <-watch.nudge:
		case <-ticker.C:
		}
		if checker != nil {
			current, checkErr := checker.OriginatingWorkState(ctx, repo, issue.Number)
			if checkErr != nil {
				if ctx.Err() == nil {
					w.logf("issue #%d closure check failed: %v", issue.Number, checkErr)
				}
			} else {
				if reason := closedWorkReason(previous, current, issue.Number); reason != "" {
					if !watch.canRelay() {
						cause := fmt.Errorf("%w: %s", errWorkClosedByUser, reason)
						w.logf("issue #%d stopping agent: %s", issue.Number, reason)
						cancel(cause)
						return
					}
					w.logf("issue #%d interrupting agent: %s; asking it to clean up in the same session", issue.Number, reason)
					cancel(&workUpdate{summary: reason, closed: true, instruction: closedWorkCleanupPrompt(issue, reason)})
					return
				}
				if watch.canRelay() && previous.IssueBody != "" && current.IssueBody != previous.IssueBody {
					w.logf("issue #%d interrupting agent: its description changed; relaying it into the same session", issue.Number)
					cancel(&workUpdate{summary: fmt.Sprintf("issue #%d description changed", issue.Number), instruction: changedDescriptionPrompt(issue)})
					return
				}
				previous = current
			}
		}
		if w.Comments == nil || !watch.canRelay() {
			continue
		}
		comments, listErr := w.Comments.ListComments(ctx, repo, issue.Number)
		if listErr != nil {
			if ctx.Err() == nil {
				w.logf("issue #%d comment check failed: %v", issue.Number, listErr)
			}
			continue
		}
		added := 0
		for _, comment := range comments {
			if relayableComment(comment, watch.since) && !seen[commentKey(comment)] {
				added++
			}
		}
		if added == 0 {
			continue
		}
		w.logf("issue #%d interrupting agent: %d new comment(s); relaying them into the same session", issue.Number, added)
		cancel(&workUpdate{summary: fmt.Sprintf("issue #%d has %d new comment(s)", issue.Number, added), instruction: newCommentsPrompt(issue, added)})
		return
	}
}

// commentsSeen snapshots the conversation a run starts from, so only what is
// posted after it counts as news for that run.
func (w *Glorp) commentsSeen(ctx context.Context, repo string, number int) map[string]bool {
	seen := map[string]bool{}
	if w.Comments == nil {
		return seen
	}
	comments, err := w.Comments.ListComments(ctx, repo, number)
	if err != nil {
		if ctx.Err() == nil {
			w.logf("issue #%d initial comment check failed: %v", number, err)
		}
		return seen
	}
	for _, comment := range comments {
		seen[commentKey(comment)] = true
	}
	return seen
}

func commentKey(comment Comment) string {
	return comment.CreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + comment.Author + "\x00" + comment.Body
}

// relayableComment reports whether a comment is worth interrupting an agent
// for. The handoff handshake (issue #214) posts claims on the same
// conversation, and every instance watching the issue reads them, so relaying
// those would restart the agent on glorp's own bookkeeping.
func relayableComment(comment Comment, since time.Time) bool {
	if _, _, ok := parseClaim(comment.Body); ok {
		return false
	}
	return !comment.CreatedAt.IsZero() && comment.CreatedAt.After(since)
}

func closedWorkCleanupPrompt(issue Issue, reason string) string {
	return fmt.Sprintf("Stop working on issue #%d: %s.\n\nDo not implement it any further and do not reopen it. Clean up what this session already started instead: close the pull request you opened for it if it is still open, delete the branch and the isolated clone if they are no longer needed, and report what you cleaned up.", issue.Number, reason)
}

func changedDescriptionPrompt(issue Issue) string {
	return fmt.Sprintf("The description of issue #%d changed while you were working on it.\n\nReread the issue from GitHub, then reconcile the work in progress with what it now asks for before continuing. Say what changed and what you adjusted.", issue.Number)
}

func newCommentsPrompt(issue Issue, added int) string {
	noun := "comment was"
	if added > 1 {
		noun = "comments were"
	}
	return fmt.Sprintf("%d new %s added to issue #%d while you were working on it.\n\nReread the issue's conversation from GitHub, treat the new comments as part of the request, and take them into account before continuing.", added, noun, issue.Number)
}

// unclosedWorkPrompt asks an agent whose run ended with the issue still open to
// carry the work the rest of the way (issue #475). Finishing a prompt is not
// finishing the ticket: an agent that timed out, stopped early, or reported
// success without merging exits exactly as one that merged does, so the
// resumed session is told what is still missing rather than a bare "continue".
func unclosedWorkPrompt(issue Issue) string {
	return fmt.Sprintf("Your run for issue #%d ended, but GitHub still reports the issue as open, so the work is not finished.\n\nPick the work back up where it stopped: check the issue and its pull request on GitHub, finish whatever remains (CI failures, review, merge), and do not stop until the issue is closed. If it genuinely cannot be closed, say exactly what is blocking it.", issue.Number)
}

// confirmIssueClosed asks GitHub whether the issue an agent just finished is
// actually closed. It reports whether the issue is closed and whether GitHub
// answered at all, so a check that could not be made is never mistaken for an
// issue that is still open. A merge GitHub has not finished processing is
// allowed for by re-reading once after the closure interval before the work is
// treated as unfinished.
func (w *Glorp) confirmIssueClosed(ctx context.Context, checker WorkClosureChecker, issue Issue) (closed, answered bool) {
	repo := issueRepository(issue.Target, issue)
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false, false
			case <-time.After(w.activeWorkClosureInterval()):
			}
		}
		state, err := checker.OriginatingWorkState(ctx, repo, issue.Number)
		if err != nil {
			if ctx.Err() != nil {
				return false, false
			}
			w.logf("issue #%d completion check failed: %v", issue.Number, err)
			continue
		}
		answered = true
		if strings.EqualFold(state.IssueState, "closed") {
			return true, true
		}
	}
	return false, answered
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
			if owner, ok := claimedByOther(comments, since, w.Identity); ok {
				cause := fmt.Errorf("%w: issue #%d claimed by another instance", errWorkClaimedByOther, issueNumber)
				w.logf("issue #%d stopping agent: instance %s claimed it on %s; letting it go", issueNumber, owner, target.describe())
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

// activeCountsByTarget counts, per configured target, the work this instance
// currently has in flight, counting only the keys parse accepts. Issues and
// discussions are dispatched on separate paths and each rotates over its own
// candidates, so each caller passes the parser for its own key shape and a
// backlog on one path never leads the other's rotation.
func activeCountsByTarget(active map[string]string, parse func(string) (string, int, bool)) map[string]int {
	counts := make(map[string]int, len(active))
	for key := range active {
		if target, _, ok := parse(key); ok {
			counts[target]++
		}
	}
	return counts
}

// balanceAcrossTargets reorders the dispatch candidates a poll produced so the
// watched targets are drawn from in turn instead of one target's whole backlog
// being queued ahead of another's (issue #464). A poll lists the targets in the
// order they were configured and appends each one's issues to a single slice,
// and the dispatch loop blocks on a concurrency slot in that same order, so
// `glorp watch repoA repoB` with 20 open issues in repoA and 3 in repoB spends
// every slot on repoA until it drains and never reaches repoB.
//
// Each target keeps its own order, and the rotation is led by whichever target
// has the least work already in flight (ties broken by configured order), so a
// target that already filled slots on an earlier poll yields the next ones to a
// target that has none. Discussion dispatch rotates over its own candidates
// through this same function (issue #467).
func balanceAcrossTargets(pending []pendingIssue, inFlight map[string]int) []pendingIssue {
	if len(pending) < 2 {
		return pending
	}
	order := make([]string, 0, len(pending))
	queues := make(map[string][]pendingIssue, len(pending))
	for _, candidate := range pending {
		target := candidate.issue.Target
		if _, ok := queues[target]; !ok {
			order = append(order, target)
		}
		queues[target] = append(queues[target], candidate)
	}
	if len(order) < 2 {
		return pending
	}
	slices.SortStableFunc(order, func(a, b string) int { return inFlight[a] - inFlight[b] })
	balanced := make([]pendingIssue, 0, len(pending))
	for len(balanced) < len(pending) {
		for _, target := range order {
			queue := queues[target]
			if len(queue) == 0 {
				continue
			}
			balanced = append(balanced, queue[0])
			queues[target] = queue[1:]
		}
	}
	return balanced
}

func issueKey(issue Issue) string {
	target := issue.Target
	if target == "" {
		target = issue.Repository
	}
	return target + "#" + strconv.Itoa(issue.Number)
}

// parseIssueWorkKey splits a key built by issueKey back into its configured
// target and issue number. Discussion keys carry an extra "#discussion"
// segment and name a thread rather than an issue, so they are rejected.
func parseIssueWorkKey(key string) (target string, number int, ok bool) {
	index := strings.LastIndex(key, "#")
	if index < 0 {
		return "", 0, false
	}
	target = key[:index]
	if strings.HasSuffix(target, "#discussion") {
		return "", 0, false
	}
	number, err := strconv.Atoi(key[index+1:])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	return target, number, true
}

// discussionWorkKey names an in-flight discussion thread. The extra segment is
// what keeps a discussion out of the issue dispatch rotation and its own work
// key distinct from the issue that happens to share its number.
func discussionWorkKey(target string, number int) string {
	return target + "#discussion#" + strconv.Itoa(number)
}

// parseDiscussionWorkKey splits a key built by discussionWorkKey back into its
// configured target and discussion number. Issue keys carry no "#discussion"
// segment and are rejected, mirroring parseIssueWorkKey.
func parseDiscussionWorkKey(key string) (target string, number int, ok bool) {
	index := strings.LastIndex(key, "#")
	if index < 0 {
		return "", 0, false
	}
	target = strings.TrimSuffix(key[:index], "#discussion")
	if target == key[:index] {
		return "", 0, false
	}
	number, err := strconv.Atoi(key[index+1:])
	if err != nil || number <= 0 {
		return "", 0, false
	}
	return target, number, true
}

// activeIssuesForRepo lists, in ascending order, the issue numbers this
// instance is actively working in repo. Work keys are scoped by configured
// target, which may be a project URL rather than a repository, so each key
// is resolved back to its repository before comparison instead of being
// string-matched against repo directly.
func activeIssuesForRepo(active map[string]string, repo string) []int {
	var numbers []int
	for key := range active {
		target, number, ok := parseIssueWorkKey(key)
		if !ok {
			continue
		}
		if !strings.EqualFold(issueRepository(target, Issue{}), repo) {
			continue
		}
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	return numbers
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
	// Run defaults Out to io.Discard, but handoff logging also runs from code
	// paths exercised without it; a missing writer must not panic.
	if w.Out != nil {
		fmt.Fprintln(w.Out, line)
	}
	if w.UI != nil {
		w.UI.Log(line)
	}
}

// logChanged logs a line only when the state it reports differs from the state
// last logged under the same key, and reports whether it logged. Polling
// otherwise repeats an unchanged summary, and an unchanged failure, on every
// tick (issue #413).
func (w *Glorp) logChanged(key, state, format string, args ...interface{}) bool {
	w.repeatMu.Lock()
	if previous, ok := w.lastLogged[key]; ok && previous == state {
		w.repeatMu.Unlock()
		return false
	}
	if w.lastLogged == nil {
		w.lastLogged = map[string]string{}
	}
	w.lastLogged[key] = state
	w.repeatMu.Unlock()
	w.logf(format, args...)
	return true
}

// forgetLogged drops the state remembered for key so the next logChanged with
// it logs again, and reports whether anything was remembered. That answer is
// how a poll that succeeded knows a failure was reported for it earlier.
func (w *Glorp) forgetLogged(key string) bool {
	w.repeatMu.Lock()
	defer w.repeatMu.Unlock()
	_, ok := w.lastLogged[key]
	delete(w.lastLogged, key)
	return ok
}

// pollStallIntervals is how many poll intervals may pass with no poll
// completing before the run reports that it has stopped making progress. A
// poll that finds nothing new logs nothing, so a wedged loop and a quiet one
// read the same in the terminal; this is what tells them apart (issue #472).
const pollStallIntervals = 4

// watchPollProgress reports a run whose poll loop has stopped completing polls,
// and reports it once rather than on every check. Every GitHub read the loop
// makes is bounded, so a stall this long means something else is holding it;
// saying so is what keeps a watch that has stopped dispatching from looking
// merely idle.
func (w *Glorp) watchPollProgress(ctx context.Context, interval time.Duration, started time.Time, lastPoll func() time.Time) {
	if interval <= 0 {
		return
	}
	stalledAfter := interval * pollStallIntervals
	if stalledAfter < time.Minute {
		stalledAfter = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		last := lastPoll()
		if last.IsZero() {
			last = started
		}
		if idle := time.Since(last); idle >= stalledAfter {
			w.logChanged("poll-stall", "stalled", "no poll has completed in %s; the watch is not picking up new work", formatInterval(idle.Round(time.Second)))
		} else if w.forgetLogged("poll-stall") {
			w.logf("polling resumed; the stall reported above is over")
		}
	}
}

func (w *Glorp) Run(ctx context.Context) error {
	// Ownership handshakes wait out their grace window off the poll loop, so
	// a run that is shutting down waits for them here rather than leaving
	// them posting comments after it has returned. Cancelling ctx cuts the
	// wait short, so this does not hold a shutdown open for the full window.
	defer w.awaitNegotiations()
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
	w.logf("watching %s (instance %s, %s, concurrency %d; %d handled issue(s) loaded)", strings.Join(targets, ", "), w.Identity, w.watchDescription(), w.Concurrency, len(seen))
	sem := newConcurrencySemaphore(w.Concurrency)
	var wg sync.WaitGroup
	var tasks taskState
	var workMu sync.Mutex
	active := make(map[string]string)
	cancellations := make(map[string]context.CancelCauseFunc)
	// nudges lets a webhook delivery about an issue an agent is working ask
	// that run's watcher to check GitHub now rather than on its next tick, so
	// push mode relays the change as promptly as it dispatches new work
	// (issue #469). The delivery is only a nudge: what actually interrupts the
	// run is still read back from GitHub.
	nudges := make(map[string]chan struct{})
	directMentions := make(map[string]bool)
	workFinished := make(chan struct{}, 1)
	jobs := make(map[string]JobSnapshot)
	issueCounts := make(map[string]int)
	// lastPoll is when the most recent poll of GitHub finished, which the UIs
	// report so a run that has stopped logging every tick (issue #413) can
	// still be seen checking (issue #447). Guarded by jobMu, since publish
	// reads it from whichever goroutine reports a job change.
	var lastPoll time.Time
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
		polled := lastPoll
		jobMu.Unlock()
		slices.SortFunc(list, func(a, b JobSnapshot) int { return b.Started.Compare(a.Started) })
		if len(list) > maxVisibleJobs {
			list = list[:maxVisibleJobs]
		}
		var quotas map[string]string
		if w.Quota != nil {
			quotas = w.Quota(ctx)
		}
		w.UI.Snapshot(GlorpSnapshot{Targets: targets, IssueCounts: counts, Running: running, Queued: queued, Completed: completed, Failed: failed, Concurrency: w.Concurrency, Interval: w.Interval, UseWebhooks: w.UseWebhooks, WebhookOnline: w.UseWebhooks, LastPoll: polled, Quotas: quotas, Jobs: list})
	}
	pollNumber := 0
	// observed records the "repo#number" keys returned by the most recent
	// poll. Webhook follow-up refreshes exist only to outlast GitHub's issue
	// index lag (issue #176), so once the delivered issue shows up here the
	// remaining refreshes have nothing left to catch. Only the run loop's own
	// goroutine reads or writes it.
	var observed map[string]bool
	pollOnce := func(sweep map[string]bool) error {
		pollNumber++
		n := pollNumber
		running, queued, _, _ := tasks.snapshot()
		// Publish before listing: browser mode consults this snapshot while
		// extracting, to avoid fetching metadata for work already in flight
		// or already finished.
		workMu.Lock()
		w.publishHandledIssues(work, active)
		workMu.Unlock()
		issues := make([]Issue, 0)
		for _, target := range targets {
			if isDiscussionTarget(target) {
				continue
			}
			batch, err := w.Issues.ListIssues(ctx, target)
			if err != nil {
				return fmt.Errorf("listing %s: %w", target, err)
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
		w.logChanged("poll-issues", strconv.Itoa(len(issues)), "poll #%d found %d open issue(s)", n, len(issues))
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
			mentionKey := issue.Repository + "#" + strconv.Itoa(issue.Number)
			directMention := directMentions[mentionKey]
			swept := sweep[key] || sweep[mentionKey]
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
			if issue.Number > 0 && (wasFailed || (staleRestoredState && remoteIssueAllowsDispatch(issue.Target, issue, w.ReadyState)) || shouldDispatchIssue(issue.Target, issue, isActive, wasActive, wasCompleted, seen[key], w.ReadyState) || (swept && !isActive && remoteIssueAllowsDispatch(issue.Target, issue, w.ReadyState)) || (directMention && !isActive)) {
				seen[key] = true
				delete(restored, key)
				delete(directMentions, mentionKey)
				// A known local failure or an active resume is unambiguously
				// this instance's own work; anything else reappearing after
				// having a local record is a reclaim that must be negotiated
				// through the comment protocol before dispatch. A project item
				// sitting at "In Progress" that this instance has no record of
				// is another instance's apparent work (typically stranded by
				// one that died mid-run), so it must be negotiated too.
				contested := (hadLocalRecord && !wasFailed && !wasActive) || (!hadLocalRecord && (projectItemInProgress(issue.Target, issue) || swept))
				session := AgentSession{
					ID: state.SessionID, Agent: state.Agent, CheckoutDirectory: state.CheckoutDirectory,
					// Persisted work is not an active worker after a daemon restart or
					// a prior failure. If it has a complete session identity, resume it
					// so the agent can recover the existing draft PR and worktree.
					Resume: state.SessionID != "" && state.Agent != "",
				}
				// Work state persisted by an earlier run must not pin the issue
				// to an agent the current configuration no longer dispatches to
				// (issue #358). Drop the stale identity so the dispatch below
				// selects a currently configured agent and starts a fresh
				// session instead of resuming with the retired one.
				if session.Agent != "" && !w.agentStillConfigured(session.Agent) {
					w.logf("issue #%d discarded persisted agent %q; it is no longer configured", issue.Number, session.Agent)
					session.ID, session.Agent, session.Resume = "", "", false
				}
				if directMention {
					// A direct mention is a new threaded instruction. Start a fresh
					// gh-fix invocation so it receives the identity argument and
					// rereads the issue and PR conversation from GitHub.
					session = AgentSession{}
					contested = false
				}
				newIssues = append(newIssues, pendingIssue{issue: issue, contested: contested, session: session})
			}
		}
		newIssues = w.negotiateContestedIssues(ctx, closureChecker, newIssues, seen, n == 1)
		workMu.Lock()
		inFlight := activeCountsByTarget(active, parseIssueWorkKey)
		workMu.Unlock()
		newIssues = balanceAcrossTargets(newIssues, inFlight)
		workMu.Lock()
		err = saveScopedWorkState(w.StatePath, work, targets)
		workMu.Unlock()
		if err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
		if len(newIssues) == 0 {
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
				if identified, ok := w.runner().(AgentIdentifier); ok {
					session.Agent = identified.AgentName()
				}
				// Claude accepts a caller-provided session ID. Other runners retain
				// the historical generated ID unless they replace it after launch.
				if agentProvider(session.Agent) != "codex" {
					session.ID, err = newSessionID()
					if err != nil {
						w.releasePendingClaim(ctx, pending, "creating its session identifier failed, so it was never dispatched")
						return err
					}
				}
			}
			if w.Status != nil {
				if err := w.Status.SetIssueStatus(ctx, issue.Target, issue, "In Progress"); err != nil {
					w.logf("issue #%d not dispatched; failed to set project status: %v", issue.Number, err)
					w.releasePendingClaim(ctx, pending, "setting the project status failed, so it was never dispatched")
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
				} else {
					w.logf("issue #%d picked up uncontested; claimed it as %s (%q)", issue.Number, w.Identity, startingClaimBody)
				}
			}
			workMu.Lock()
			key := issueKey(issue)
			active[key] = session.ID
			spec, _ := parseAgentSpec(session.Agent)
			jobMu.Lock()
			jobs[key] = JobSnapshot{Number: issue.Number, Target: issue.Target, Title: issue.Title, Status: "queued", CheckoutDirectory: session.CheckoutDirectory, SessionID: session.ID, Agent: spec.Name, Model: spec.Model, Effort: spec.Level, Started: time.Now()}
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
			if err := w.acquireSlot(ctx, sem); err != nil {
				tasks.mu.Lock()
				tasks.queued--
				tasks.mu.Unlock()
				return ctx.Err()
			}
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
			startedRunning, startedQueued := running, queued
			wg.Add(1)
			go func(i Issue, agentSession AgentSession, running, queued int) {
				defer wg.Done()
				defer sem.release()
				defer func() {
					select {
					case workFinished <- struct{}{}:
					default:
					}
				}()
				key := issueKey(i)
				nudge := make(chan struct{}, 1)
				workMu.Lock()
				nudges[key] = nudge
				workMu.Unlock()
				defer func() {
					workMu.Lock()
					delete(nudges, key)
					workMu.Unlock()
				}()
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
				// A run that changes nothing about the ticket runs once. One
				// interrupted because the issue changed under it is resumed in
				// place with what changed, so the agent already holding the
				// work receives it rather than the change waiting out the run
				// (issue #469).
				alreadyClosed := false
				keepalives := 0
				for {
					runCtx, cancelRun := context.WithCancelCause(ctx)
					workMu.Lock()
					cancellations[key] = cancelRun
					workMu.Unlock()
					watch := issueWatch{since: time.Now(), closed: alreadyClosed, nudge: nudge, resumable: func() bool {
						workMu.Lock()
						defer workMu.Unlock()
						state := work[key]
						return state.SessionID != "" && state.Agent != ""
					}}
					var closureReady <-chan struct{}
					if closureChecker != nil || w.Comments != nil {
						ready := make(chan struct{})
						closureReady = ready
						go w.watchForIssueUpdates(runCtx, closureChecker, i, watch, cancelRun, ready)
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
					activeRunner := w.runner()
					if cause := context.Cause(runCtx); isCooperativeCancellation(cause) || errors.Is(cause, errWorkStoppedFromWebUI) || errors.Is(cause, errWorkUpdated) {
						runErr = cause
					} else if w.UI != nil {
						if runner, ok := activeRunner.(SessionAgentOutputRunner); ok {
							runErr = runner.RunSessionWithOutput(runCtx, i, agentSession, updateSession, jobOutput)
						} else if runner, ok := activeRunner.(AgentOutputRunner); ok {
							runErr = runner.RunWithOutput(runCtx, i, jobOutput)
						} else {
							runErr = activeRunner.Run(runCtx, i)
						}
					} else if runner, ok := activeRunner.(SessionAgentRunner); ok {
						runErr = runner.RunSession(runCtx, i, agentSession, updateSession)
					} else {
						runErr = activeRunner.Run(runCtx, i)
					}
					cause := context.Cause(runCtx)
					if isCooperativeCancellation(cause) || errors.Is(cause, errWorkStoppedFromWebUI) || errors.Is(cause, errWorkUpdated) {
						runErr = cause
					}
					cancelRun(nil)
					workMu.Lock()
					delete(cancellations, key)
					state := work[key]
					workMu.Unlock()
					update, updated := workUpdateFor(cause)
					if !updated || ctx.Err() != nil {
						if runErr != nil || ctx.Err() != nil || closureChecker == nil {
							break
						}
						// The agent finishing its prompt is not the work being
						// done (issue #475): one that timed out or stopped
						// early returns exactly as one that merged its pull
						// request does. The run only counts as complete once
						// GitHub says the issue is closed; while it is still
						// open the work is kept alive and continued instead of
						// being reported as finished.
						closed, answered := w.confirmIssueClosed(ctx, closureChecker, i)
						if closed || !answered || ctx.Err() != nil {
							break
						}
						keepalives++
						if state.SessionID == "" || state.Agent == "" {
							w.logf("issue #%d keepalive: the agent finished but the issue is still open, and its session cannot be resumed; restarting the agent (attempt %d)", i.Number, keepalives)
							agentSession.Resume = false
							agentSession.Update = ""
						} else {
							w.logf("issue #%d keepalive: the agent finished but the issue is still open; continuing session %s (attempt %d)", i.Number, state.SessionID, keepalives)
							agentSession = AgentSession{ID: state.SessionID, Agent: state.Agent, CheckoutDirectory: state.CheckoutDirectory, Resume: true, Update: unclosedWorkPrompt(i)}
						}
						runErr = nil
						publish()
						continue
					}
					// The session identity is read back from the work state so
					// a run whose agent reported its own session ID after
					// launch is resumed with that one rather than the
					// placeholder it was dispatched with.
					if state.SessionID == "" || state.Agent == "" {
						w.logf("issue #%d cannot resume its session to deliver the update; stopping instead", i.Number)
						break
					}
					alreadyClosed = alreadyClosed || update.closed
					agentSession = AgentSession{ID: state.SessionID, Agent: state.Agent, CheckoutDirectory: state.CheckoutDirectory, Resume: true, Update: update.instruction}
					runErr = nil
					w.logf("issue #%d resuming session %s with the update", i.Number, state.SessionID)
					publish()
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
	// poll runs one poll and reports the end of a run of failures, so a poll
	// loop that recovers says so once even though the failures it recovered
	// from were only reported once (issue #413).
	poll := func(sweep map[string]bool) error {
		err := pollOnce(sweep)
		if err == nil {
			// Record and publish the check itself, not just what it dispatched:
			// a poll that finds nothing new logs nothing, so this timestamp is
			// what tells the user the run is still alive (issue #447).
			jobMu.Lock()
			lastPoll = time.Now()
			jobMu.Unlock()
			publish()
			if w.forgetLogged("poll-error") {
				w.logf("poll #%d succeeded; the failure reported above is resolved", pollNumber)
			}
		}
		return err
	}
	// reportPollError reports a failed poll once. Every poll that fails does so
	// through here, so the same failure repeating on every tick is
	// reported when it starts rather than on every tick, and the listing or
	// state error that caused it is reported by this line alone instead of
	// being logged a second time where it was raised (issue #413).
	reportPollError := func(kind string, err error) {
		if kind != "" {
			kind += " "
		}
		w.logChanged("poll-error", err.Error(), "%spoll #%d error: %v; retrying in %s", kind, pollNumber, err, w.periodicPollInterval())
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
		workMu.Lock()
		numbers := activeIssuesForRepo(active, event.Repository)
		workMu.Unlock()
		owned := slices.Contains(numbers, event.IssueNumber)
		// Once a draft pull request exists the handshake moves to it
		// (ownershipTargetFor), so the ask names the pull request number and
		// never matches an active issue number. Checking the open pull
		// requests of the work already in flight is what keeps a busy
		// instance from silently losing its branch (issue #363).
		if !owned && closureChecker != nil {
			for _, number := range numbers {
				state, err := closureChecker.OriginatingWorkState(ctx, event.Repository, number)
				if err != nil {
					w.logf("issue #%d failed to check whether pull request #%d continues it: %v", number, event.IssueNumber, err)
					continue
				}
				for _, pullRequest := range state.PullRequests {
					if pullRequest.Number != event.IssueNumber || pullRequest.Merged || strings.EqualFold(pullRequest.State, "closed") {
						continue
					}
					owned = true
					w.logf("issue #%d instance %s asked who owns pull request #%d; it continues our active work", number, id, event.IssueNumber)
					break
				}
				if owned {
					break
				}
			}
		}
		if !owned {
			w.logf("issue #%d instance %s asked who owns it; not ours, staying quiet", event.IssueNumber, id)
			return
		}
		w.logf("issue #%d instance %s asked who owns it; answering %q as %s", event.IssueNumber, id, presenceClaimBody, w.Identity)
		if err := w.Comments.PostComment(ctx, event.Repository, event.IssueNumber, signComment(presenceClaimBody, w.Identity)); err != nil {
			w.logf("issue #%d failed to respond to ownership ask: %v", event.IssueNumber, err)
		}
	}
	if err := poll(nil); err != nil {
		if ctx.Err() != nil {
			wg.Wait()
			w.logf("stopped during initial poll")
			return nil
		}
		reportPollError("initial", err)
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
					w.logChanged("board-probe:"+target, err.Error(), "project board probe for %s failed: %v", target, err)
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
	go w.watchPollProgress(watchCtx, w.periodicPollInterval(), time.Now(), func() time.Time {
		jobMu.Lock()
		defer jobMu.Unlock()
		return lastPoll
	})
	var tick <-chan time.Time
	var retryTimer *time.Timer
	var retry <-chan time.Time
	retriesRemaining := 0
	// pendingWebhookIssue names the issue whose delivery scheduled the current
	// follow-up chain, so the chain can stop early once a refresh sees it.
	pendingWebhookIssue := ""
	// pendingWebhookSweep preserves referenced continuation work across the
	// follow-up chain, including while GitHub's issue/dependency indexes lag.
	var pendingWebhookSweep map[string]bool
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
	if pushed := w.pushedBoardTargets(targets); len(pushed) > 0 {
		w.logf("not probing %d project board(s) that push board changes over webhooks: %s", len(pushed), strings.Join(pushed, ", "))
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
	retryAfterStop := make(map[string]bool)
	for {
		select {
		case request := <-w.settingsRequestsChan():
			request.done <- w.applySettingsRequest(request.update, sem)
		case request := <-w.jobActionRequests():
			action := request.action
			key := action.Target + "#" + strconv.Itoa(action.Number)
			jobMu.Lock()
			job, exists := jobs[key]
			jobMu.Unlock()
			if !exists {
				request.done <- fmt.Errorf("job not found")
				continue
			}
			switch action.Action {
			case "stop":
				if job.Status != "active" {
					request.done <- fmt.Errorf("job cannot be stopped while %s", job.Status)
					continue
				}
				workMu.Lock()
				cancel := cancellations[key]
				workMu.Unlock()
				if cancel == nil {
					request.done <- fmt.Errorf("job is not running")
					continue
				}
				w.logf("issue #%d stop requested from web UI", action.Number)
				jobMu.Lock()
				job.Status = "stopping"
				jobs[key] = job
				jobMu.Unlock()
				publish()
				cancel(errWorkStoppedFromWebUI)
				request.done <- nil
			case "retry":
				switch job.Status {
				case "active":
					workMu.Lock()
					cancel := cancellations[key]
					workMu.Unlock()
					if cancel == nil {
						request.done <- fmt.Errorf("job is not running")
						continue
					}
					w.logf("issue #%d retry requested from web UI; stopping and rerunning gh-fix", action.Number)
					retryAfterStop[key] = true
					jobMu.Lock()
					job.Status = "stopping"
					jobs[key] = job
					jobMu.Unlock()
					publish()
					cancel(errWorkStoppedFromWebUI)
					request.done <- nil
				case "failed", "complete":
					w.logf("issue #%d retry requested from web UI; rerunning gh-fix", action.Number)
					workMu.Lock()
					state := work[key]
					state.Status = "failed"
					work[key] = state
					workMu.Unlock()
					jobMu.Lock()
					job.Status = "failed"
					jobs[key] = job
					jobMu.Unlock()
					request.done <- poll(nil)
				default:
					request.done <- fmt.Errorf("job cannot be retried while %s", job.Status)
				}
			default:
				request.done <- fmt.Errorf("unknown job action %q", action.Action)
			}
		case <-ctx.Done():
			w.logf("shutdown requested; waiting for running tasks to finish")
			wg.Wait()
			running, queued, completed, failed := tasks.snapshot()
			w.logf("stopped (tasks: %d running, %d queued, %d completed, %d failed)", running, queued, completed, failed)
			return nil
		case <-w.negotiatedNudges():
			// A background handshake just won its ticket. Poll now so the
			// reclaimed work is dispatched instead of waiting out a tick that
			// may be an hour away, or the next reap.
			if err := poll(nil); err != nil && ctx.Err() == nil {
				reportPollError("handoff", err)
			}
		case <-tick:
			if w.Webhooks != nil {
				w.Webhooks(ctx)
			}
			if err := poll(nil); err != nil {
				if ctx.Err() != nil {
					w.logf("shutdown requested during poll; waiting for running tasks to finish")
					wg.Wait()
					w.logf("stopped")
					return nil
				}
				reportPollError("", err)
			}
		case <-boardTick:
			if !probeBoards(ctx) {
				continue
			}
			if err := poll(nil); err != nil && ctx.Err() == nil {
				reportPollError("project board", err)
			}
		case <-reap:
			w.logf("periodic reap started")
			if err := poll(nil); err != nil && ctx.Err() == nil {
				reportPollError("reap", err)
			}
		case event := <-w.Events:
			w.logWebhookEvent(event)
			respondToOwnershipAsk(ctx, event)
			// A delivery about an issue an agent is already working is not new
			// work to dispatch; it is a change that agent needs to hear about,
			// so its watcher is asked to check GitHub now (issue #469).
			if webhookEventUpdatesActiveWork(event) {
				workMu.Lock()
				pending := nudgesForIssue(nudges, event.Repository, event.IssueNumber)
				workMu.Unlock()
				for _, nudge := range pending {
					select {
					case nudge <- struct{}{}:
						w.logf("issue #%d %s delivery concerns work in flight; checking it now", event.IssueNumber, webhookEventLabel(event))
					default:
					}
				}
			}
			if event.Kind == "issue_comment" && event.Action == "created" && mentionedIdentity(event.CommentBody, w.Identity) {
				// A mention in the webhook payload alone is not enough to trigger a
				// run (issue #294): the mentioning comment must also be the current
				// last comment and come from an allowed commenter, both reverified
				// against GitHub rather than trusted from the delivery.
				authorized := commenterAllowed(event.CommentAuthor, w.AllowedCommenters)
				if authorized && w.Comments != nil {
					var err error
					authorized, err = authorizedDirectMention(ctx, w.Comments, event.Repository, event.IssueNumber, w.Identity, w.AllowedCommenters)
					if err != nil {
						w.logf("issue #%d failed to verify direct mention of instance %s: %v", event.IssueNumber, w.Identity, err)
						authorized = false
					}
				}
				if !authorized {
					w.logf("issue #%d ignoring direct mention of instance %s: not the last comment or not from an allowed commenter", event.IssueNumber, w.Identity)
				} else if key, ok := webhookIssueKey(event); ok {
					directMentions[key] = true
					w.logf("issue #%d directly mentioned instance %s; refreshing for a threaded gh-fix run", event.IssueNumber, w.Identity)
					if err := poll(nil); err != nil && ctx.Err() == nil {
						reportPollError("direct-mention", err)
					}
					// A direct mention is intentionally not an ordinary comment refresh:
					// it must survive a lagging issue search (or a transient list
					// failure) until the mentioned issue is actually dispatched. The
					// normal webhook retry chain stops as soon as an issue is merely
					// observed, which is insufficient here because local completed or
					// active state can still defer the fresh threaded run.
					if directMentions[key] {
						pendingWebhookIssue = key
						retriesRemaining = webhookRetryLimit
						if retryTimer == nil {
							retryTimer = time.NewTimer(w.Interval)
							retry = retryTimer.C
							w.logf("issue #%d direct mention is still pending; scheduling follow-up refresh", event.IssueNumber)
						} else {
							w.logf("issue #%d direct mention is still pending; using the scheduled follow-up refresh", event.IssueNumber)
						}
					}
					continue
				}
			}
			// The payload alone decides whether this delivery could have
			// changed dispatchable work. Pushes, pull request activity, and
			// ordinary comments never do, so they no longer cost a refresh.
			if !webhookEventNeedsRefresh(event) {
				w.logf("webhook %s delivery cannot change issue state; skipping refresh", webhookEventLabel(event))
				continue
			}
			sweep := webhookContinuationSweep(event)
			if len(sweep) > 0 {
				keys := slices.Collect(maps.Keys(sweep))
				slices.Sort(keys)
				w.logf("webhook %s delivery sweeping referenced work: %s", webhookEventLabel(event), strings.Join(keys, ", "))
				if pendingWebhookSweep == nil {
					pendingWebhookSweep = make(map[string]bool, len(sweep))
				}
				for key := range sweep {
					pendingWebhookSweep[key] = true
				}
			}
			if err := poll(sweep); err != nil {
				if ctx.Err() != nil {
					wg.Wait()
					return nil
				}
				reportPollError("webhook-triggered", err)
			}
			// Keep an already scheduled follow-up refresh. GitHub may deliver
			// another webhook while its issue index is still catching up; resetting
			// the timer in that case can make the refresh observe the previous
			// issue and miss the newest one until another delivery arrives.
			if retryTimer == nil {
				if key, ok := webhookIssueKey(event); ok && observed[key] {
					// The refresh above already saw the delivered issue, so
					// there is no index lag left for follow-ups to outlast.
					pendingWebhookSweep = nil
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
			if err := poll(pendingWebhookSweep); err != nil && ctx.Err() == nil {
				reportPollError("webhook follow-up", err)
			}
			retriesRemaining--
			if pendingWebhookIssue != "" && !directMentions[pendingWebhookIssue] && observed[pendingWebhookIssue] {
				w.logf("webhook follow-up refreshes complete; issue %s observed", pendingWebhookIssue)
				retriesRemaining = 0
			}
			if retriesRemaining > 0 {
				retryTimer = time.NewTimer(w.Interval)
				retry = retryTimer.C
			} else {
				pendingWebhookIssue = ""
				pendingWebhookSweep = nil
				retry = nil
			}
		case <-workFinished:
			for key := range retryAfterStop {
				jobMu.Lock()
				status := jobs[key].Status
				jobMu.Unlock()
				if status == "failed" {
					delete(retryAfterStop, key)
					if err := poll(nil); err != nil && ctx.Err() == nil {
						reportPollError("retry", err)
					}
				}
			}
			if len(directMentions) > 0 {
				if err := poll(nil); err != nil && ctx.Err() == nil {
					reportPollError("queued direct-mention", err)
				}
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
			if err := poll(nil); err != nil && ctx.Err() == nil {
				reportPollError("state reload", err)
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
// issues are dispatchable, using only the payload glorp already decoded. A
// closed pull request can unblock referenced work even when it does not close
// an issue, so it refreshes immediately. Push, ping, other pull request
// activity, and comments do not. Unrecognized event kinds refresh so a newly
// subscribed event is never silently dropped.
func webhookEventNeedsRefresh(event WebhookEvent) bool {
	switch event.Kind {
	case "push", "ping", "issue_comment":
		return false
	case "pull_request":
		return event.Action == "closed"
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

// webhookEventUpdatesActiveWork reports whether a delivery could have changed
// the issue itself under an agent already working it (issue #469). These are
// deliberately the deliveries webhookEventNeedsRefresh ignores: an edited
// description and a new comment change nothing about which issues are
// dispatchable, but they change what the agent holding this one was asked to
// do. A closure is both, and counts here as well.
func webhookEventUpdatesActiveWork(event WebhookEvent) bool {
	if event.Repository == "" || event.IssueNumber <= 0 {
		return false
	}
	switch event.Kind {
	case "issue_comment":
		return event.Action == "created" || event.Action == "edited"
	case "issues":
		return event.Action == "edited" || event.Action == "closed"
	default:
		return false
	}
}

// nudgesForIssue returns the nudge channels of the runs working repo#number.
// The keys are target scoped, so a project board target watching the same
// issue is matched by the repository it resolves to rather than by the key.
func nudgesForIssue(nudges map[string]chan struct{}, repo string, number int) []chan struct{} {
	if repo == "" || number <= 0 {
		return nil
	}
	var matched []chan struct{}
	for key, nudge := range nudges {
		target, keyNumber, ok := parseIssueWorkKey(key)
		if !ok || keyNumber != number || !strings.EqualFold(issueRepository(target, Issue{}), repo) {
			continue
		}
		matched = append(matched, nudge)
	}
	return matched
}

// webhookContinuationSweep identifies referenced issues that should be
// reconsidered as continuation work when an issue or pull request closes.
func webhookContinuationSweep(event WebhookEvent) map[string]bool {
	if event.Repository == "" || event.Action != "closed" || (event.Kind != "issues" && event.Kind != "pull_request") {
		return nil
	}
	sweep := make(map[string]bool, len(event.MentionedIssues))
	for _, number := range event.MentionedIssues {
		if number <= 0 || (event.Kind == "issues" && number == event.IssueNumber) {
			continue
		}
		sweep[event.Repository+"#"+strconv.Itoa(number)] = true
	}
	return sweep
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

func issueNumbers(issues []Issue) string {
	numbers := make([]string, len(issues))
	for i, issue := range issues {
		numbers[i] = fmt.Sprintf("#%d", issue.Number)
	}
	return strings.Join(numbers, ", ")
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

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create agent session: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
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

func isProjectTarget(repo string) bool { return core.IsProjectTarget(repo) }

func isDiscussionTarget(repo string) bool { return core.IsDiscussionTarget(repo) }

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
	watched := make(map[string]bool, len(targets))
	for _, target := range targets {
		watched[target] = true
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
			separator := strings.LastIndexByte(key, '#')
			if len(watched) > 0 && (separator <= 0 || !watched[key[:separator]]) {
				continue
			}
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
				return fmt.Errorf("invalid scoped state key %q", key)
			}
		}
		value = legacy
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

// The dispatch predicates live in package core, shared with the browser
// driver's board reader.
func issueBlocked(issue Issue) (bool, string) { return core.IssueBlocked(issue) }

func shouldDispatchIssue(repo string, issue Issue, isActive, wasActive, wasCompleted, seen bool, readyState string) bool {
	return core.ShouldDispatchIssue(repo, issue, isActive, wasActive, wasCompleted, seen, readyState)
}

func projectItemInProgress(target string, issue Issue) bool {
	return core.ProjectItemInProgress(target, issue)
}

func remoteIssueAllowsDispatch(target string, issue Issue, readyState string) bool {
	return core.RemoteIssueAllowsDispatch(target, issue, readyState)
}

func projectStatusAllowsDispatch(status, readyState string) bool {
	return core.ProjectStatusAllowsDispatch(status, readyState)
}

func projectReadyState(configured, current string) string {
	return core.ProjectReadyState(configured, current)
}
