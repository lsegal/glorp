package core

import "testing"

func TestWakeParked(t *testing.T) {
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

	blocked := ParkedWork{Kind: ParkedBlocked, WaitingOn: []int{12}}
	stack := ParkedWork{Kind: ParkedStacked, WaitingOn: []int{12}, PullRequests: []int{15}}
	pullRequest := ParkedWork{Kind: ParkedPullRequest, PullRequests: []int{15}}
	for _, test := range []struct {
		name   string
		parked ParkedWork
		check  ParkedCheck
		want   ParkedWake
	}{
		{"blocked waits on an open blocker", blocked, ParkedCheck{Issue: blockedIssue}, ParkedStay},
		{"blocked wakes when its blocker closes", blocked, ParkedCheck{Issue: unblockedIssue}, ParkedResume},
		{"blocked finishes when its issue closes", blocked, ParkedCheck{Issue: Issue{Number: 7, State: "closed"}}, ParkedComplete},
		{"stacked waits while its blocker pull request is open", stack, ParkedCheck{Issue: blockedIssue, Own: stacked, Blockers: blockerOpen}, ParkedStay},
		{"stacked wakes when its blocker issue closes", stack, ParkedCheck{Issue: unblockedIssue, Own: stacked}, ParkedResume},
		{"stacked wakes when its blocker pull request merges", stack, ParkedCheck{Issue: blockedIssue, Own: stacked, Blockers: blockerMerged}, ParkedResume},
		{"stacked wakes when its blocker pull request closes", stack, ParkedCheck{Issue: blockedIssue, Own: stacked, Blockers: blockerClosed}, ParkedResume},
		{"stacked restarts when its own pull request closes unmerged", stack, ParkedCheck{Issue: blockedIssue, Own: rejected, Blockers: blockerOpen}, ParkedRestart},
		{"stacked without a readable blocker stays", stack, ParkedCheck{Issue: blockedIssue}, ParkedStay},
		{"legacy record is stacked", ParkedWork{}, ParkedCheck{Issue: unblockedIssue}, ParkedResume},
		{"held pull request waits while open", pullRequest, ParkedCheck{Issue: Issue{Number: 7}, Own: held}, ParkedStay},
		{"held pull request completes when it merges", pullRequest, ParkedCheck{Issue: Issue{Number: 7}, Own: merged}, ParkedComplete},
		{"held pull request completes when its issue closes", pullRequest, ParkedCheck{Issue: Issue{Number: 7}, Own: closedIssue}, ParkedComplete},
		{"held pull request restarts when closed unmerged", pullRequest, ParkedCheck{Issue: Issue{Number: 7}, Own: rejected}, ParkedRestart},
		{"held pull request never wakes on unreadable state", pullRequest, ParkedCheck{Issue: Issue{Number: 7}}, ParkedStay},
	} {
		got, reason := WakeParked(test.parked, test.check)
		if got != test.want {
			t.Errorf("%s: WakeParked() = %v (%q), want %v", test.name, got, reason, test.want)
		}
		if reason == "" {
			t.Errorf("%s: WakeParked() gave no reason", test.name)
		}
	}
}

// Review activity changes nothing a wake check reads: an approved pull request
// with changes requested and new comments is still open and unmerged, so it
// leaves every kind of parked work exactly where it was.
func TestWakeParkedIgnoresReviewActivity(t *testing.T) {
	reviewed := &OriginatingWorkState{IssueState: "open", IssueBody: "edited", PullRequests: []PullRequestWorkState{{Number: 15, State: "open", Stacked: true}}}
	issue := Issue{Number: 7, State: "open", DependsOn: []IssueDependency{{Number: 12, State: "open"}}}
	blockers := map[int]OriginatingWorkState{12: {IssueState: "open", PullRequests: []PullRequestWorkState{{Number: 14, State: "open"}}}}
	for _, kind := range []string{ParkedBlocked, ParkedStacked, ParkedPullRequest} {
		if got, reason := WakeParked(ParkedWork{Kind: kind}, ParkedCheck{Issue: issue, Own: reviewed, Blockers: blockers}); got != ParkedStay {
			t.Errorf("%s: WakeParked() = %v (%q), want it to stay parked", kind, got, reason)
		}
	}
}
