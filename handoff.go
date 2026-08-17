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

// signComment appends the instance identity signature used to attribute a
// handoff comment to a specific glorp instance.
func signComment(body string, id Identity) string {
	return fmt.Sprintf("%s /glorp:%s", body, id)
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
// other than self. Per the handoff protocol, the most recent claim wins, so
// any such comment means another instance has already taken ownership.
func claimedByOther(comments []Comment, after time.Time, self Identity) bool {
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
			return true
		}
	}
	return false
}

// ownershipTarget identifies where a handoff negotiation should take place:
// the issue itself, or an already open draft pull request linked to it.
type ownershipTarget struct {
	Repo     string
	Number   int
	Continue bool
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
// reaping work that already looks claimed (an agent-started label or an
// open draft pull request with no local record of ownership). It asks,
// waits at least ownershipWaitDuration, and only claims the work if no
// other instance answered or claimed it first. The last instance to post a
// starting/continuing claim always wins.
func (w *Glorp) negotiateOwnership(ctx context.Context, target ownershipTarget) (bool, error) {
	if w.Comments == nil {
		return true, nil
	}
	askedAt := time.Now()
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
	if claimedByOther(comments, askedAt, w.Identity) {
		return false, nil
	}
	claimBody := startingClaimBody
	if target.Continue {
		claimBody = continuingClaimBody
	}
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
