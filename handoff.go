package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Identity uniquely names one running glorp instance. It lives only in
// memory: a restarted instance gets a new identity, which is why ownership
// of previously claimed work must be renegotiated rather than assumed.
type Identity string

func newIdentity() (Identity, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create instance identity: %w", err)
	}
	return Identity(strings.ToUpper(hex.EncodeToString(b))), nil
}

// Comment is a single issue or pull request comment relevant to the
// cooperative handoff protocol.
type Comment struct {
	Body      string
	Author    string
	CreatedAt time.Time
}

// CommentPoster posts a comment to an issue or pull request.
type CommentPoster interface {
	PostComment(ctx context.Context, repo string, number int, body string) error
}

// CommentLister lists the comments on an issue or pull request.
type CommentLister interface {
	ListComments(ctx context.Context, repo string, number int) ([]Comment, error)
}

// CommentClient is the combined capability needed to run the handoff
// handshake described in issue #214.
type CommentClient interface {
	CommentPoster
	CommentLister
}

// ownershipWaitDuration is the minimum grace period a reaping instance must
// wait after asking "does anyone have this?" before claiming abandoned work.
const ownershipWaitDuration = 2 * time.Minute

// staleClaimDuration is how old the newest claim from another instance must
// be before a periodic reap treats the work as abandoned and re-opens the
// "does anyone have this?" handshake. Reaps run repeatedly (issue #239), so
// without this the handshake would be re-posted on every pass and spam
// issues that a live instance is still working.
const staleClaimDuration = 2 * time.Hour

const (
	askClaimBody        = "Does anyone have this?"
	startingClaimBody   = "Starting work on this issue"
	continuingClaimBody = "Continuing work on this issue"
	presenceClaimBody   = "I am working on this"
)

type claimKind int

const (
	claimUnknown claimKind = iota
	claimAsking
	claimStarting
	claimContinuing
	claimPresence
)

// identityPattern matches the trailing "/glorp:UUID" signature that every
// handoff comment must carry.
var identityPattern = regexp.MustCompile(`/glorp:(\S+)\s*$`)

// mentionedIdentity reports whether a comment directly addresses this glorp
// instance using the public @/glorp:ID meta-mention syntax. Delimiters prevent
// one instance ID from matching a longer ID that merely shares its prefix.
func mentionedIdentity(body string, id Identity) bool {
	if id == "" {
		return false
	}
	pattern := `(^|\s)@/glorp:` + regexp.QuoteMeta(string(id)) + `($|\s|[.,!?;:])`
	return regexp.MustCompile(pattern).MatchString(body)
}

// signComment appends the instance identity signature used to attribute a
// handoff comment to a specific glorp instance.
func signComment(body string, id Identity) string {
	return fmt.Sprintf("%s /glorp:%s", body, id)
}

// commenterAllowed reports whether login may trigger a direct-mention gh-fix
// run. An empty allowed list places no restriction on the author (issue
// #294 has the CLI default this to the authenticated gh user).
func commenterAllowed(login string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	if login == "" {
		return false
	}
	for _, candidate := range allowed {
		if strings.EqualFold(candidate, login) {
			return true
		}
	}
	return false
}

// authorizedDirectMention reports whether the most recent comment on
// repo#number is a safe, actionable direct mention of this glorp instance
// (issue #294): it must be the very last comment, so a stale mention buried
// under newer conversation cannot silently retrigger a run; it must contain
// the @/glorp:ID mention syntax addressed to id; and it must be posted by an
// allowed commenter.
func authorizedDirectMention(ctx context.Context, comments CommentLister, repo string, number int, id Identity, allowed []string) (bool, error) {
	list, err := comments.ListComments(ctx, repo, number)
	if err != nil {
		return false, err
	}
	if len(list) == 0 {
		return false, nil
	}
	last := list[len(list)-1]
	return mentionedIdentity(last.Body, id) && commenterAllowed(last.Author, allowed), nil
}

// parseClaim classifies a comment body as one of the handoff protocol
// messages and extracts the signing identity. It returns ok=false for
// comments that are not part of the handoff protocol.
func parseClaim(body string) (kind claimKind, id Identity, ok bool) {
	trimmed := strings.TrimSpace(body)
	match := identityPattern.FindStringSubmatch(trimmed)
	if match == nil {
		return claimUnknown, "", false
	}
	id = Identity(match[1])
	text := strings.TrimSpace(strings.TrimSuffix(trimmed, match[0]))
	switch {
	case strings.HasPrefix(text, askClaimBody):
		kind = claimAsking
	case strings.HasPrefix(text, startingClaimBody):
		kind = claimStarting
	case strings.HasPrefix(text, continuingClaimBody):
		kind = claimContinuing
	case strings.HasPrefix(text, presenceClaimBody):
		kind = claimPresence
	default:
		return claimUnknown, "", false
	}
	return kind, id, true
}

// claimedByOther reports whether any comment created at or after the given
// time is a starting, continuing, or presence claim signed by an identity
// other than self, and returns that identity so the caller can name the
// winning instance in its logs. Per the handoff protocol, the most recent
// claim wins, so any such comment means another instance has already taken
// ownership.
func claimedByOther(comments []Comment, after time.Time, self Identity) (Identity, bool) {
	for _, comment := range comments {
		if comment.CreatedAt.Before(after) {
			continue
		}
		kind, id, ok := parseClaim(comment.Body)
		if !ok || id == self {
			continue
		}
		switch kind {
		case claimStarting, claimContinuing, claimPresence:
			return id, true
		}
	}
	return "", false
}

// latestClaimByOther returns the creation time and signing identity of the
// most recent starting, continuing, or presence claim signed by an identity
// other than self.
func latestClaimByOther(comments []Comment, self Identity) (time.Time, Identity, bool) {
	var latest time.Time
	var owner Identity
	found := false
	for _, comment := range comments {
		kind, id, ok := parseClaim(comment.Body)
		if !ok || id == self {
			continue
		}
		switch kind {
		case claimStarting, claimContinuing, claimPresence:
			if !found || comment.CreatedAt.After(latest) {
				latest = comment.CreatedAt
				owner = id
				found = true
			}
		}
	}
	return latest, owner, found
}

// claimIsFresh reports whether another instance has claimed the target
// recently enough that a periodic reap should leave it alone. Work with no
// foreign claim at all, or one older than staleClaimDuration, is fair game
// for the "does anyone have this?" handshake.
// It also reports who holds that claim and how old it is, so callers can say
// in the log why a reap was skipped or why work was treated as abandoned.
func (w *Glorp) claimIsFresh(ctx context.Context, target ownershipTarget) (fresh bool, owner Identity, age time.Duration, err error) {
	if w.Comments == nil {
		return false, "", 0, nil
	}
	comments, err := w.Comments.ListComments(ctx, target.Repo, target.Number)
	if err != nil {
		return false, "", 0, err
	}
	claimedAt, owner, ok := latestClaimByOther(comments, w.Identity)
	if !ok {
		return false, "", 0, nil
	}
	age = time.Since(claimedAt)
	return age < w.staleClaimAfter(), owner, age, nil
}

// ownershipTarget identifies where a handoff negotiation should take place:
// the issue itself, or an already open draft pull request linked to it.
type ownershipTarget struct {
	Repo     string
	Number   int
	Continue bool
}

// describe names the target the way handoff log lines refer to it, so a
// reader can tell whether a handshake happened on the issue itself or on an
// already open pull request continuing that work.
func (t ownershipTarget) describe() string {
	if t.Continue {
		return fmt.Sprintf("pull request %s#%d", t.Repo, t.Number)
	}
	return fmt.Sprintf("issue %s#%d", t.Repo, t.Number)
}

// ownershipTargetFor resolves whether an issue already has an open,
// unmerged pull request associated with it. When it does, the handoff
// protocol negotiates on the pull request instead of the issue and resumes
// that branch rather than starting over.
func ownershipTargetFor(ctx context.Context, checker WorkClosureChecker, issue Issue) ownershipTarget {
	repo := issueRepository(issue.Target, issue)
	target := ownershipTarget{Repo: repo, Number: issue.Number}
	if checker == nil {
		return target
	}
	state, err := checker.OriginatingWorkState(ctx, repo, issue.Number)
	if err != nil {
		return target
	}
	for _, pullRequest := range state.PullRequests {
		if pullRequest.Merged || strings.EqualFold(pullRequest.State, "closed") {
			continue
		}
		target.Number = pullRequest.Number
		target.Continue = true
		return target
	}
	return target
}

// negotiateOwnership runs the "does anyone have this?" handshake before
// reaping work that already looks claimed (a reappearing issue or an open
// draft pull request with no local record of ownership). It asks,
// waits at least ownershipWaitDuration, and only claims the work if no
// other instance answered or claimed it first. The last instance to post a
// starting/continuing claim always wins.
func (w *Glorp) negotiateOwnership(ctx context.Context, target ownershipTarget) (bool, error) {
	if w.Comments == nil {
		return true, nil
	}
	askedAt := time.Now()
	w.logf("%s asking %q as %s; waiting %s for another instance to answer", target.describe(), askClaimBody, w.Identity, ownershipWaitDuration)
	if err := w.Comments.PostComment(ctx, target.Repo, target.Number, signComment(askClaimBody, w.Identity)); err != nil {
		return false, err
	}
	if !w.awaitOwnershipWindow(ctx) {
		return false, ctx.Err()
	}
	comments, err := w.Comments.ListComments(ctx, target.Repo, target.Number)
	if err != nil {
		return false, err
	}
	if owner, ok := claimedByOther(comments, askedAt, w.Identity); ok {
		w.logf("%s answered by instance %s during the handoff window; letting it go", target.describe(), owner)
		return false, nil
	}
	claimBody := startingClaimBody
	if target.Continue {
		claimBody = continuingClaimBody
	}
	w.logf("%s unanswered after %s; claiming it as %s (%q)", target.describe(), ownershipWaitDuration, w.Identity, claimBody)
	return true, w.Comments.PostComment(ctx, target.Repo, target.Number, signComment(claimBody, w.Identity))
}

// awaitOwnershipWindow blocks for the handoff grace period, or returns
// false early if the context is cancelled first. Tests override
// ownershipWait to avoid real sleeps.
func (w *Glorp) awaitOwnershipWindow(ctx context.Context) bool {
	if w.ownershipWait != nil {
		return w.ownershipWait(ctx)
	}
	timer := time.NewTimer(ownershipWaitDuration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// claimComment builds the initial, uncontested ownership comment for an
// issue that no other instance appears to have touched yet.
func claimComment(id Identity, continuing bool) string {
	body := startingClaimBody
	if continuing {
		body = continuingClaimBody
	}
	return signComment(body, id)
}
