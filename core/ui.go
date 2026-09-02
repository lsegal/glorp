package core

import "time"

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
	TokensUsed    int
	TokenLimit    int
	Quota         string
	Quotas        map[string]string
	Jobs          []JobSnapshot
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
	Agent             *string   `json:"agent,omitempty"`
}

// SettingsSnapshot reports the settings a running glorp instance can change
// on the fly, and their current values.
type SettingsSnapshot struct {
	Concurrency       int      `json:"concurrency"`
	ReadyState        string   `json:"readyState"`
	ReadyStateDefault string   `json:"readyStateDefault"`
	AllowedCommenters []string `json:"allowedCommenters"`
	Agent             string   `json:"agent"`
	Agents            []string `json:"agents"`
}
