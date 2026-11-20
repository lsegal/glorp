package core

import (
	"encoding/json"
	"time"
)

// Job event kinds (issue #715).
const (
	// JobEventText is a message the agent wrote.
	JobEventText = "text"
	// JobEventTool is a tool call the agent made.
	JobEventTool = "tool"
	// JobEventToolResult is the outcome of an earlier tool call, matched to
	// it by ToolID.
	JobEventToolResult = "tool_result"
	// JobEventUsage reports the tokens a run used.
	JobEventUsage = "usage"
	// JobEventError is an error the agent or glorp reported for the run.
	JobEventError = "error"
	// JobEventOutput is a line of output that is not one of the agent's
	// events: a banner, a warning, or a note glorp adds itself.
	JobEventOutput = "output"
	// JobEventComment is a GitHub comment sent to the job.
	JobEventComment = "comment"
)

// JobEvent is one typed event of a job's agent output, served by the
// dashboard's events endpoint so a client can read a job's output
// incrementally instead of diffing its whole Log (issue #715).
type JobEvent struct {
	// Seq numbers a job's events in order. It only grows, including across
	// the job's runs, so it works as a cursor.
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
	Kind string    `json:"kind"`
	// Text is a message's text, an output or error line, or an excerpt of a
	// tool result.
	Text string `json:"text,omitempty"`
	// Tool, ToolID, and Input describe a tool call, and Summary is the
	// progress line the Log shows for it. A tool result carries the ToolID of
	// its call and IsError when the call failed.
	Tool    string          `json:"tool,omitempty"`
	ToolID  string          `json:"toolID,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Summary string          `json:"summary,omitempty"`
	IsError bool            `json:"isError,omitempty"`
	Usage   *TokenUsage     `json:"usage,omitempty"`
	Comment *JobComment     `json:"comment,omitempty"`
}

// TokenUsage is what a run reported spending.
type TokenUsage struct {
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int64   `json:"cacheCreationTokens,omitempty"`
	CostUSD             float64 `json:"costUSD,omitempty"`
}

// JobComment is a GitHub comment sent to a job.
type JobComment struct {
	Author     string `json:"author,omitempty"`
	URL        string `json:"url"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	// PullRequest is set when the comment is on a pull request rather than
	// on the issue.
	PullRequest bool `json:"pullRequest,omitempty"`
}

// JobEvents is the events endpoint's answer: the events after the requested
// cursor, oldest first.
type JobEvents struct {
	// Run counts the job's runs. Each new run starts its events over, so a
	// client seeing Run change drops what it kept from the previous one.
	Run int `json:"run"`
	// Latest is the newest event's Seq, the cursor for the next request.
	Latest int64      `json:"latest"`
	Events []JobEvent `json:"events"`
	// Truncated is set when events after the cursor were already dropped by
	// the per-job retention bound.
	Truncated bool `json:"truncated,omitempty"`
}
