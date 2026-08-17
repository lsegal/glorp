package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func TestSignAndParseClaim(t *testing.T) {
	body := signComment(startingClaimBody, "AAA111")
	kind, id, ok := parseClaim(body)
	if !ok {
		t.Fatalf("expected claim to parse: %q", body)
	}
	if kind != claimStarting {
		t.Fatalf("kind = %v, want claimStarting", kind)
	}
	if id != "AAA111" {
		t.Fatalf("id = %q, want AAA111", id)
	}
}

func TestParseClaimVariants(t *testing.T) {
	cases := []struct {
		body string
		kind claimKind
		ok   bool
	}{
		{signComment(askClaimBody, "X"), claimAsking, true},
		{signComment(startingClaimBody, "X"), claimStarting, true},
		{signComment(continuingClaimBody, "X"), claimContinuing, true},
		{signComment(presenceClaimBody, "X"), claimPresence, true},
		{"just a regular comment", claimUnknown, false},
		{"Starting work on this issue with no signature", claimUnknown, false},
	}
	for _, tc := range cases {
		kind, _, ok := parseClaim(tc.body)
		if ok != tc.ok || (ok && kind != tc.kind) {
			t.Errorf("parseClaim(%q) = (%v, %v), want (%v, %v)", tc.body, kind, ok, tc.kind, tc.ok)
		}
	}
}

func TestClaimedByOtherIgnoresOwnAndOldComments(t *testing.T) {
	after := time.Now()
	comments := []Comment{
		{Body: signComment(startingClaimBody, "SELF"), CreatedAt: after.Add(time.Second)},
		{Body: signComment(startingClaimBody, "OTHER"), CreatedAt: after.Add(-time.Second)},
	}
	if claimedByOther(comments, after, "SELF") {
		t.Fatalf("expected no other claimant: own comment and a stale comment shouldn't count")
	}
	comments = append(comments, Comment{Body: signComment(presenceClaimBody, "OTHER"), CreatedAt: after.Add(time.Minute)})
	if !claimedByOther(comments, after, "SELF") {
		t.Fatalf("expected a fresh presence claim from another identity to be detected")
	}
}

func TestClaimedByOtherIgnoresAskComments(t *testing.T) {
	after := time.Now()
	comments := []Comment{
		{Body: signComment(askClaimBody, "OTHER"), CreatedAt: after.Add(time.Second)},
	}
	if claimedByOther(comments, after, "SELF") {
		t.Fatalf("an ask comment alone should not count as a claim")
	}
}

type fakeCommentClient struct {
	mu       sync.Mutex
	comments map[string][]Comment
	posts    int
	postErr  error
	listErr  error
}

func newFakeCommentClient() *fakeCommentClient {
	return &fakeCommentClient{comments: make(map[string][]Comment)}
}

func (f *fakeCommentClient) key(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

func (f *fakeCommentClient) PostComment(_ context.Context, repo string, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return f.postErr
	}
	f.posts++
	key := f.key(repo, number)
	f.comments[key] = append(f.comments[key], Comment{Body: body, CreatedAt: time.Now()})
	return nil
}

func (f *fakeCommentClient) ListComments(_ context.Context, repo string, number int) ([]Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	key := f.key(repo, number)
	out := make([]Comment, len(f.comments[key]))
	copy(out, f.comments[key])
	return out, nil
}

func (f *fakeCommentClient) inject(repo string, number int, comment Comment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(repo, number)
	f.comments[key] = append(f.comments[key], comment)
}

func TestNegotiateOwnershipClaimsWhenUncontested(t *testing.T) {
	comments := newFakeCommentClient()
	w := &Glorp{Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool { return true }}
	claimed, err := w.negotiateOwnership(context.Background(), ownershipTarget{Repo: "o/r", Number: 1})
	if err != nil {
		t.Fatalf("negotiateOwnership: %v", err)
	}
	if !claimed {
		t.Fatalf("expected to claim uncontested issue")
	}
	posted, _ := comments.ListComments(context.Background(), "o/r", 1)
	if len(posted) != 2 {
		t.Fatalf("expected an ask comment and a starting comment, got %d", len(posted))
	}
	if kind, _, ok := parseClaim(posted[0].Body); !ok || kind != claimAsking {
		t.Fatalf("first comment should be the ask, got %q", posted[0].Body)
	}
	if kind, id, ok := parseClaim(posted[1].Body); !ok || kind != claimStarting || id != "SELF" {
		t.Fatalf("second comment should be our starting claim, got %q", posted[1].Body)
	}
}

func TestNegotiateOwnershipUsesContinuingClaimForDraftPR(t *testing.T) {
	comments := newFakeCommentClient()
	w := &Glorp{Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool { return true }}
	claimed, err := w.negotiateOwnership(context.Background(), ownershipTarget{Repo: "o/r", Number: 5, Continue: true})
	if err != nil || !claimed {
		t.Fatalf("negotiateOwnership: claimed=%v err=%v", claimed, err)
	}
	posted, _ := comments.ListComments(context.Background(), "o/r", 5)
	if kind, _, ok := parseClaim(posted[len(posted)-1].Body); !ok || kind != claimContinuing {
		t.Fatalf("expected a continuing claim, got %q", posted[len(posted)-1].Body)
	}
}

func TestNegotiateOwnershipStandsDownWhenAnotherInstanceResponds(t *testing.T) {
	comments := newFakeCommentClient()
	waited := false
	w := &Glorp{Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool {
		// Simulate another instance answering while we wait.
		comments.inject("o/r", 1, Comment{Body: signComment(presenceClaimBody, "OTHER"), CreatedAt: time.Now()})
		waited = true
		return true
	}}
	claimed, err := w.negotiateOwnership(context.Background(), ownershipTarget{Repo: "o/r", Number: 1})
	if err != nil {
		t.Fatalf("negotiateOwnership: %v", err)
	}
	if !waited {
		t.Fatalf("expected the wait hook to run")
	}
	if claimed {
		t.Fatalf("expected to stand down after another instance responded")
	}
	posted, _ := comments.ListComments(context.Background(), "o/r", 1)
	for _, comment := range posted {
		if kind, id, ok := parseClaim(comment.Body); ok && kind == claimStarting && id == "SELF" {
			t.Fatalf("should not have posted a starting claim after standing down")
		}
	}
}

func TestNegotiateOwnershipStandsDownOnCompetingStartingClaim(t *testing.T) {
	comments := newFakeCommentClient()
	w := &Glorp{Comments: comments, Identity: "SELF", ownershipWait: func(context.Context) bool {
		comments.inject("o/r", 1, Comment{Body: signComment(startingClaimBody, "LATER"), CreatedAt: time.Now()})
		return true
	}}
	claimed, err := w.negotiateOwnership(context.Background(), ownershipTarget{Repo: "o/r", Number: 1})
	if err != nil || claimed {
		t.Fatalf("claimed=%v err=%v, want claimed=false", claimed, err)
	}
}

func TestNegotiateOwnershipReturnsFalseWhenContextCancelledDuringWait(t *testing.T) {
	comments := newFakeCommentClient()
	ctx, cancel := context.WithCancel(context.Background())
	w := &Glorp{Comments: comments, Identity: "SELF", ownershipWait: func(ctx context.Context) bool {
		cancel()
		return false
	}}
	claimed, err := w.negotiateOwnership(ctx, ownershipTarget{Repo: "o/r", Number: 1})
	if claimed {
		t.Fatalf("should not claim when the wait is interrupted")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNegotiateOwnershipNoopWithoutCommentClient(t *testing.T) {
	w := &Glorp{}
	claimed, err := w.negotiateOwnership(context.Background(), ownershipTarget{Repo: "o/r", Number: 1})
	if err != nil || !claimed {
		t.Fatalf("expected an instance without comment support to claim immediately, got claimed=%v err=%v", claimed, err)
	}
}

func TestOwnershipTargetForPrefersOpenPullRequest(t *testing.T) {
	checker := &fakeClosureSource{state: OriginatingWorkState{
		IssueState: "open",
		PullRequests: []PullRequestWorkState{
			{Number: 9, State: "closed", Merged: false},
			{Number: 10, State: "open", Merged: false},
		},
	}}
	target := ownershipTargetFor(context.Background(), checker, Issue{Number: 1, Repository: "o/r", Target: "o/r"})
	if !target.Continue || target.Number != 10 {
		t.Fatalf("target = %+v, want continue on PR #10", target)
	}
}

func TestOwnershipTargetForFallsBackToIssueWithoutOpenPullRequest(t *testing.T) {
	checker := &fakeClosureSource{state: OriginatingWorkState{IssueState: "open"}}
	target := ownershipTargetFor(context.Background(), checker, Issue{Number: 3, Repository: "o/r", Target: "o/r"})
	if target.Continue || target.Number != 3 {
		t.Fatalf("target = %+v, want issue #3 without continue", target)
	}
}

func TestNegotiateContestedIssuesFiltersDeclinedClaims(t *testing.T) {
	comments := newFakeCommentClient()
	// Issue #1 is contested and another instance answers; issue #2 is
	// uncontested so it passes straight through without negotiation.
	comments.inject("o/r", 1, Comment{})
	w := &Glorp{Comments: comments, Identity: "SELF", Out: io.Discard, ownershipWait: func(context.Context) bool {
		comments.inject("o/r", 1, Comment{Body: signComment(presenceClaimBody, "OTHER"), CreatedAt: time.Now()})
		return true
	}}
	pending := []pendingIssue{
		{issue: Issue{Number: 1, Repository: "o/r", Target: "o/r"}, contested: true},
		{issue: Issue{Number: 2, Repository: "o/r", Target: "o/r"}},
	}
	declinedKey := issueKey(pending[0].issue)
	seen := map[string]bool{declinedKey: true, issueKey(pending[1].issue): true}
	result := w.negotiateContestedIssues(context.Background(), nil, pending, seen)
	if len(result) != 1 || result[0].issue.Number != 2 {
		t.Fatalf("result = %+v, want only issue #2 to survive", result)
	}
	if seen[declinedKey] {
		t.Fatalf("declined issue #1 should be unmarked as seen so it is retried")
	}
}

func TestNegotiateContestedIssuesSkipsResumedSessions(t *testing.T) {
	comments := newFakeCommentClient()
	w := &Glorp{Comments: comments, Identity: "SELF"}
	pending := []pendingIssue{
		{issue: Issue{Number: 1, Repository: "o/r", Target: "o/r"}, contested: true, session: AgentSession{Resume: true}},
	}
	result := w.negotiateContestedIssues(context.Background(), nil, pending, map[string]bool{})
	if len(result) != 1 {
		t.Fatalf("resumed local sessions should skip negotiation entirely, got %+v", result)
	}
	if posted, _ := comments.ListComments(context.Background(), "o/r", 1); len(posted) != 0 {
		t.Fatalf("expected no comments posted for a resumed session, got %v", posted)
	}
}
