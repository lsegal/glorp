// Package core holds the types glorp's halves exchange: the issues and
// comments a source produces, the state a run reads back from GitHub, and the
// target syntax that names where either comes from. It is deliberately small
// and dependency free, so the browser driver in package browser and the root
// package can both speak them without importing each other.
package core

import (
	"context"
	"time"
)

// Issue is a GitHub issue as glorp reads it, whichever source it came from.
type Issue struct {
	Number        int               `json:"number"`
	Repository    string            `json:"repository,omitempty"`
	Title         string            `json:"title,omitempty"`
	Body          string            `json:"body,omitempty"`
	State         string            `json:"state,omitempty"`
	CreatedAt     time.Time         `json:"createdAt,omitempty"`
	Labels        []IssueLabel      `json:"labels,omitempty"`
	DependsOn     []IssueDependency `json:"dependsOn,omitempty"`
	HasSubIssues  bool              `json:"hasSubIssues,omitempty"`
	ProjectStatus string            `json:"projectStatus,omitempty"`
	ProjectItemID string            `json:"-"`
	Target        string            `json:"-"`
}

// IssueLabel is one label on an issue.
type IssueLabel struct {
	Name string `json:"name"`
}

// IssueDependency is an issue this one is blocked by.
type IssueDependency struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

// IssueSource lists the issues a watched target currently offers.
type IssueSource interface {
	ListIssues(context.Context, string) ([]Issue, error)
}

// IssueRepository resolves the OWNER/REPO an issue belongs to, falling back to
// the target it was listed from when the issue does not name one itself.
func IssueRepository(target string, issue Issue) string {
	if issue.Repository != "" {
		return issue.Repository
	}
	parsed, err := ParseTarget(target)
	if err == nil && parsed.Repo != "" {
		return parsed.Repo
	}
	return target
}

// PullRequestWorkState is one pull request linked to an issue an agent is
// working, and how far along it is.
type PullRequestWorkState struct {
	Number int
	State  string
	Merged bool
}

// OriginatingWorkState is what a run reads back about the issue an agent is
// working, so a closure or an edit made while it runs can be relayed into the
// session.
type OriginatingWorkState struct {
	IssueState string
	// IssueBody is the issue's description, compared between checks so an
	// edit made while an agent is working is relayed into its session
	// (issue #469).
	IssueBody    string
	PullRequests []PullRequestWorkState
}

// WorkClosureChecker reports the current state of the issue behind active work.
type WorkClosureChecker interface {
	OriginatingWorkState(context.Context, string, int) (OriginatingWorkState, error)
}
