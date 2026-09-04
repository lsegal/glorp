package core

import (
	"errors"
	"time"
)

// ErrNotReady is returned by the settings and job-action handlers when the
// run loop has not yet reached its dispatch select statement. Before that
// point the handlers would otherwise block indefinitely on an unbuffered
// channel nothing is reading from yet, leaving the dashboard's "loading
// settings..." spinner stuck with no error and no timeout (issue #579). The
// dashboard treats this as a signal to retry rather than a hard failure.
var ErrNotReady = errors.New("run is still starting")

// JobSnapshot is one agent run as the dashboards draw it.
type JobSnapshot struct {
	Number            int
	Target            string
	Title             string
	Status            string
	CheckoutDirectory string
	SessionID         string
	Agent             string
	Model             string
	Effort            string
	Started           time.Time
	Log               string
}

// Snapshot is the whole run as the dashboards draw it. The terminal dashboard
// in the root package and the browser dashboard in package webui both render
// it, so it lives here rather than in either of them.
type Snapshot struct {
	Targets     []string
	IssueCounts map[string]int
	Running     int
	Queued      int
	Completed   int
	Failed      int
	Concurrency int
	NextPoll    time.Time
	// LastPoll is when the most recent poll of GitHub finished. The poll loop
	// only logs what it found when that changes (issue #413), so this is the
	// run's standing evidence that it is still checking (issue #447).
	LastPoll      time.Time
	Interval      time.Duration
	UseWebhooks   bool
	WebhookURL    string
	WebhookOnline bool
	// WebUIURL is the local browser dashboard address when it is enabled.
	// The terminal dashboard shows it as a persistent bottom line so operators
	// can open the companion interface without searching the startup logs.
	WebUIURL   string
	TokensUsed int
	TokenLimit int
	Quota      string
	Quotas     map[string]string
	Jobs       []JobSnapshot
}

// UIReporter receives the published state of a run.
type UIReporter interface {
	Snapshot(Snapshot)
	Log(string)
}

// JobAction is a retry or stop a dashboard asks the run to perform on one job.
type JobAction struct {
	Action string `json:"action"`
	Target string `json:"target"`
	Number int    `json:"number"`
}

// SettingsUpdate carries a partial update to glorp's live-editable runtime
// settings (issue #341). Nil fields are left unchanged.
type SettingsUpdate struct {
	Concurrency       *int      `json:"concurrency,omitempty"`
	ReadyState        *string   `json:"readyState,omitempty"`
	AllowedCommenters *[]string `json:"allowedCommenters,omitempty"`
	// ActiveAgents replaces the live set of agent specs new dispatches
	// round-robin across (issue #572), overriding what --agent configured at
	// startup. When given it must name at least one valid agent spec; the
	// result is reported back as SettingsSnapshot.ConfiguredAgents.
	ActiveAgents *[]string `json:"activeAgents,omitempty"`
}

// AgentOption describes one agent the registry knows, so the dashboard's
// agent selector can offer it -- and the models and levels it accepts -- from
// what the run actually registered rather than from a hardcoded pair.
type AgentOption struct {
	Name string `json:"name"`
	// Models and Levels are the agent's allow-lists. Empty means the agent
	// accepts any value, so the UI offers no closed list for it.
	Models []string `json:"models,omitempty"`
	Levels []string `json:"levels,omitempty"`
}

// SettingsSnapshot reports the settings a running glorp instance can change
// on the fly, and their current values.
type SettingsSnapshot struct {
	Concurrency       int      `json:"concurrency"`
	ReadyState        string   `json:"readyState"`
	ReadyStateDefault string   `json:"readyStateDefault"`
	AllowedCommenters []string `json:"allowedCommenters"`
	// Agents lists every agent the run's registry defines, built-in and
	// config-defined alike, which is the set ActiveAgents may be set from.
	Agents []string `json:"agents"`
	// AgentOptions carries the same agents with their model and level
	// allow-lists, for a selector that offers more than a bare name.
	AgentOptions []AgentOption `json:"agentOptions,omitempty"`
	// ConfiguredAgents lists the agent specs this run dispatches with, which
	// is a subset of Agents chosen by --agent or the live ActiveAgents
	// override (issue #572).
	ConfiguredAgents []string `json:"configuredAgents"`
}

// AgentStatus reports one registered agent's install, sign-in, and quota
// state, mirroring what `glorp agents` prints, for the settings dashboard's
// agents tab (issue #572).
type AgentStatus struct {
	Name        string `json:"name"`
	Installed   bool   `json:"installed"`
	Binary      string `json:"binary"`
	Version     string `json:"version,omitempty"`
	VersionNote string `json:"versionNote,omitempty"`
	Auth        string `json:"auth"`
	Quota       string `json:"quota"`
	// TracksQuota says the definition names a quota source at all, telling an
	// agent that reports no quota by design apart from one whose reader
	// could not answer -- the same distinction describeQuota draws for the
	// terminal report.
	TracksQuota bool   `json:"tracksQuota"`
	QuotaError  string `json:"quotaError,omitempty"`
	// Models are the fully qualified agent/model names --agent accepts, and
	// ModelNote explains a missing or partial list.
	Models    []string `json:"models,omitempty"`
	ModelNote string   `json:"modelNote,omitempty"`
	// DefaultModel is the model glorp runs this agent with when --agent names
	// none, empty for an agent whose definition leaves the choice to its own
	// CLI (issue #612).
	DefaultModel string `json:"defaultModel,omitempty"`
	// Status is one of "ok", "warn", or "missing", matching the marker
	// `glorp agents` prefixes each block with.
	Status string `json:"status"`
}
