package core

import "testing"

func TestWakePaused(t *testing.T) {
	blockedIssue := Issue{Number: 7, State: "open", DependsOn: []IssueDependency{{Number: 12, State: "open"}}}
	unblockedIssue := Issue{Number: 7, State: "open", DependsOn: []IssueDependency{{Number: 12, State: "closed"}}}
	held := &OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "open"}}}
	stacked := &OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "open", Stacked: true}}}
	merged := &OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "closed", Merged: true}}}
	rejected := &OriginatingWorkState{IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 15, State: "closed"}}}
	closedIssue := &OriginatingWorkState{IssueState: "closed", PullRequests: []PullRequestWorkState{{Number: 15, State: "closed"}}}
	blockerOpen := map[int]OriginatingWorkState{12: {IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 14, State: "open"}}}}
	blockerMerged := map[int]OriginatingWorkState{12: {IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 14, State: "closed", Merged: true}}}}
	blockerClosed := map[int]OriginatingWorkState{12: {IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 14, State: "closed"}}}}

	blocked := PausedWork{Kind: PausedBlocked, WaitingOn: []int{12}}
	stack := PausedWork{Kind: PausedStacked, WaitingOn: []int{12}, PullRequests: []int{15}}
	pullRequest := PausedWork{Kind: PausedPullRequest, PullRequests: []int{15}}
	for _, test := range []struct {
		name   string
		paused PausedWork
		check  PausedCheck
		want   PausedWake
	}{
		{"blocked waits on an open blocker", blocked, PausedCheck{Issue: blockedIssue}, PausedStay},
		{"blocked wakes when its blocker closes", blocked, PausedCheck{Issue: unblockedIssue}, PausedResume},
		{"blocked finishes when its issue closes", blocked, PausedCheck{Issue: Issue{Number: 7, State: "closed"}}, PausedComplete},
		{"stacked waits while its blocker pull request is open", stack, PausedCheck{Issue: blockedIssue, Own: stacked, Blockers: blockerOpen}, PausedStay},
		{"stacked wakes when its blocker issue closes", stack, PausedCheck{Issue: unblockedIssue, Own: stacked}, PausedResume},
		{"stacked wakes when its blocker pull request merges", stack, PausedCheck{Issue: blockedIssue, Own: stacked, Blockers: blockerMerged}, PausedResume},
		{"stacked wakes when its blocker pull request closes", stack, PausedCheck{Issue: blockedIssue, Own: stacked, Blockers: blockerClosed}, PausedResume},
		{"stacked restarts when its own pull request closes unmerged", stack, PausedCheck{Issue: blockedIssue, Own: rejected, Blockers: blockerOpen}, PausedRestart},
		{"stacked without a readable blocker stays", stack, PausedCheck{Issue: blockedIssue}, PausedStay},
		{"legacy record is stacked", PausedWork{}, PausedCheck{Issue: unblockedIssue}, PausedResume},
		{"held pull request waits while open", pullRequest, PausedCheck{Issue: Issue{Number: 7}, Own: held}, PausedStay},
		{"held pull request completes when it merges", pullRequest, PausedCheck{Issue: Issue{Number: 7}, Own: merged}, PausedComplete},
		{"held pull request completes when its issue closes", pullRequest, PausedCheck{Issue: Issue{Number: 7}, Own: closedIssue}, PausedComplete},
		{"held pull request restarts when closed unmerged", pullRequest, PausedCheck{Issue: Issue{Number: 7}, Own: rejected}, PausedRestart},
		{"held pull request never wakes on unreadable state", pullRequest, PausedCheck{Issue: Issue{Number: 7}}, PausedStay},
	} {
		got, reason := WakePaused(test.paused, test.check)
		if got != test.want {
			t.Errorf("%s: WakePaused() = %v (%q), want %v", test.name, got, reason, test.want)
		}
		if reason == "" {
			t.Errorf("%s: WakePaused() gave no reason", test.name)
		}
	}
}

// Review activity changes nothing a wake check reads: an approved pull request
// with changes requested and new comments is still open and unmerged, so it
// leaves every kind of paused work exactly where it was.
func TestWakePausedIgnoresReviewActivity(t *testing.T) {
	reviewed := &OriginatingWorkState{IssueState: "open", IssueBody: "edited", PullRequests: []PullRequestWorkState{{Number: 15, State: "open", Stacked: true}}}
	issue := Issue{Number: 7, State: "open", DependsOn: []IssueDependency{{Number: 12, State: "open"}}}
	blockers := map[int]OriginatingWorkState{12: {IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 14, State: "open"}}}}
	for _, kind := range []string{PausedBlocked, PausedStacked, PausedPullRequest} {
		if got, reason := WakePaused(PausedWork{Kind: kind}, PausedCheck{Issue: issue, Own: reviewed, Blockers: blockers}); got != PausedStay {
			t.Errorf("%s: WakePaused() = %v (%q), want it to stay paused", kind, got, reason)
		}
	}
}
