// Package agents describes the coding-agent CLIs glorp can dispatch work to as
// data rather than as code. Every place that has to know how to talk to an
// agent -- the argv for a fresh run, a resume, and a one-shot vision call, the
// environment its child process needs, how its session ID is established, and
// how its output is decoded -- reads a Definition instead of branching on the
// agent's name, so supporting another CLI is a JSON document rather than a set
// of new `if name == ...` arms.
package agents

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Definition is everything glorp needs to know to invoke one agent CLI. It is
// unmarshalled from the built-in JSON documents embedded in this package and
// from the user's own .glorp.config.json.
type Definition struct {
	// Name is the agent name used in --agent name/model:level.
	Name string `json:"name"`
	// Binary is the default executable the agent is invoked through. It is
	// overridable per agent by the run's own binary flags (issue #489).
	Binary string `json:"binary"`
	// Args holds the argv template for each mode glorp invokes.
	Args Args `json:"args"`
	// Env is extra environment for the child process, layered on top of
	// glorp's own environment.
	Env map[string]string `json:"env,omitempty"`
	// Session says how a session ID is established and what happens to it
	// when a resume fails.
	Session Session `json:"session"`
	// Levels and Models are optional allow-lists used to validate --agent and
	// to say what was expected when it does not match. An empty list accepts
	// anything.
	Levels []string `json:"levels,omitempty"`
	Models []string `json:"models,omitempty"`
	// Output names how the agent's stdout is decoded. Only the two formats
	// glorp already decodes are understood here; pluggable decoding is issue
	// #488, which fills this section in rather than reshaping it.
	Output Output `json:"output"`
	// Quota names the quota source for the agent.
	Quota Quota `json:"quota,omitempty"`
	// Skills names the skills.sh target the agent's copy of the gh-fix and
	// gh-discuss skills is installed for, so the installers derive their
	// --agent list from the registry instead of a hand-edited one.
	Skills Skills `json:"skills,omitempty"`
}

// Args are the argv templates for the three shapes of invocation glorp makes:
// a fresh run, a resume of an earlier session, and the one-shot vision call
// browser mode makes on a screenshot.
type Args struct {
	Run    []Fragment `json:"run"`
	Resume []Fragment `json:"resume"`
	Vision []Fragment `json:"vision,omitempty"`
}

// Fragment is one conditional piece of an argv template. Its arguments are
// appended in order when its condition holds, with {placeholder} substituted
// from the invocation's values. This is deliberately a small explicit model
// rather than an expression language: a fragment can test one named value for
// presence and nothing else.
type Fragment struct {
	// When names the condition, optionally negated with a leading "!". An
	// empty condition always holds.
	When string `json:"when,omitempty"`
	// Args are the arguments contributed when the condition holds.
	Args []string `json:"args"`
}

// Session describes how the agent's session ID comes to exist.
type Session struct {
	// Assign is "glorp" when glorp generates the ID and passes it to the
	// agent (Claude's --session-id), "capture" when the agent assigns its own
	// and prints it (Codex), or "none" when the agent has no resumable
	// session at all.
	Assign string `json:"assign"`
	// Capture is the regular expression that reads the ID out of the agent's
	// stdout when Assign is "capture". Its first capture group is the ID.
	Capture string `json:"capture,omitempty"`
	// ClearOnResumeFailure drops the recorded ID when a resume fails because
	// the session is gone, so the restarted run takes a fresh one. Agents
	// glorp assigns IDs for keep theirs.
	ClearOnResumeFailure bool `json:"clearOnResumeFailure,omitempty"`
}

// Output names the decoder applied to the agent's stdout.
type Output struct {
	// Format is "text" for output shown as it is written, or "stream-json"
	// for Claude's streaming event envelope.
	Format string `json:"format"`
}

// Skills names where an agent reads glorp's skills from.
type Skills struct {
	// Target is the skills.sh target id `skills add --agent` takes: a
	// dedicated one such as "codex" or "claude-code", or "universal" for a
	// CLI skills.sh has no dedicated id for.
	Target string `json:"target,omitempty"`
}

// Quota names the quota source for the agent. An agent that declares none
// reports no quota at all, which the UI shows as untracked; that is the
// deliberate default, and it costs no process on any poll.
type Quota struct {
	// Reader selects the reader: "none" (the default), "codex", "claude", or
	// the generic "command" reader below.
	Reader string `json:"reader,omitempty"`
	// Command is the argv the "command" reader runs, whose stdout it parses
	// as JSON. The {binary} placeholder substitutes the executable the agent
	// itself was resolved to, so --agent-binary reaches the quota call too.
	Command []string `json:"command,omitempty"`
	// PercentUsed and ResetAt are dotted paths into that JSON naming the
	// percentage of the window already consumed and the time it resets.
	// Either may be omitted when the format does not reference it.
	PercentUsed string `json:"percentUsed,omitempty"`
	ResetAt     string `json:"resetAt,omitempty"`
	// Format is the status-bar template, with {percentUsed}, {percentLeft},
	// and {resetAt} substituted from the fields above.
	Format string `json:"format,omitempty"`
	// Timeout bounds one read, as a Go duration string. A quota call is a
	// status-bar nicety, so it is never allowed to hang a poll.
	Timeout string `json:"timeout,omitempty"`
}

// Quota reader names.
const (
	QuotaNone    = "none"
	QuotaCodex   = "codex"
	QuotaClaude  = "claude"
	QuotaCommand = "command"
)

// DefaultQuotaTimeout bounds one generic quota read when the definition names
// no timeout of its own.
const DefaultQuotaTimeout = 30 * time.Second

// defaultQuotaFormat is the template the generic reader renders when the
// definition names none, matching the shape the built-in readers report in.
const defaultQuotaFormat = "{percentLeft}% left"

// quotaPlaceholders is every {name} a quota format may substitute.
var quotaPlaceholders = []string{"percentUsed", "percentLeft", "resetAt"}

// quotaReaders is every reader name the schema accepts.
var quotaReaders = []string{QuotaNone, QuotaCodex, QuotaClaude, QuotaCommand}

// ReaderName is the reader in force, resolving the empty default to "none".
func (q Quota) ReaderName() string {
	if strings.TrimSpace(q.Reader) == "" {
		return QuotaNone
	}
	return q.Reader
}

// FormatTemplate is the template the generic reader renders.
func (q Quota) FormatTemplate() string {
	if strings.TrimSpace(q.Format) == "" {
		return defaultQuotaFormat
	}
	return q.Format
}

// TimeoutDuration bounds one generic quota read. Validate has already
// rejected an unparseable value, so a bad one here falls back rather than
// reporting a second time.
func (q Quota) TimeoutDuration() time.Duration {
	if strings.TrimSpace(q.Timeout) == "" {
		return DefaultQuotaTimeout
	}
	timeout, err := time.ParseDuration(q.Timeout)
	if err != nil || timeout <= 0 {
		return DefaultQuotaTimeout
	}
	return timeout
}

// Argv renders the generic reader's command, substituting {binary} with the
// executable the agent was resolved to.
func (q Quota) Argv(binary string) []string {
	argv := make([]string, 0, len(q.Command))
	for _, arg := range q.Command {
		argv = append(argv, strings.ReplaceAll(arg, "{binary}", binary))
	}
	return argv
}

// validate reports the first thing wrong with the quota block. Fields that
// belong to the generic reader are rejected on the others rather than
// ignored, because a "quota" block that silently does nothing looks exactly
// like a working one until the status bar stays empty.
func (q Quota) validate() error {
	reader := q.ReaderName()
	if !contains(quotaReaders, reader) {
		return fmt.Errorf(`field "quota.reader": %q must be %s`, q.Reader, JoinOr(quotaReaders))
	}
	if reader != QuotaCommand {
		for field, set := range map[string]bool{
			"quota.command": len(q.Command) > 0, "quota.percentUsed": q.PercentUsed != "",
			"quota.resetAt": q.ResetAt != "", "quota.format": q.Format != "",
			"quota.timeout": q.Timeout != "",
		} {
			if set {
				return fmt.Errorf(`field %q is only meaningful when "quota.reader" is %q`, field, QuotaCommand)
			}
		}
		return nil
	}
	if len(q.Command) == 0 {
		return fmt.Errorf(`field "quota.command" is required when "quota.reader" is %q`, QuotaCommand)
	}
	for _, arg := range q.Command {
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf(`field "quota.command" cannot contain an empty argument`)
		}
	}
	for _, match := range placeholderPattern.FindAllStringSubmatch(q.FormatTemplate(), -1) {
		if !contains(quotaPlaceholders, match[1]) {
			return fmt.Errorf(`field "quota.format": unknown placeholder %s; known placeholders are {%s}`, match[0], strings.Join(sorted(quotaPlaceholders), "}, {"))
		}
	}
	if q.PercentUsed == "" && strings.Contains(q.FormatTemplate(), "{percent") {
		return fmt.Errorf(`field "quota.percentUsed" is required when "quota.format" substitutes a percentage`)
	}
	if q.ResetAt == "" && strings.Contains(q.FormatTemplate(), "{resetAt}") {
		return fmt.Errorf(`field "quota.resetAt" is required when "quota.format" substitutes {resetAt}`)
	}
	if strings.TrimSpace(q.Timeout) != "" {
		timeout, err := time.ParseDuration(q.Timeout)
		if err != nil {
			return fmt.Errorf(`field "quota.timeout": %w`, err)
		}
		if timeout <= 0 {
			return fmt.Errorf(`field "quota.timeout": %q must be positive`, q.Timeout)
		}
	}
	return nil
}

// Assign values.
const (
	AssignGlorp   = "glorp"
	AssignCapture = "capture"
	AssignNone    = "none"
)

// Output formats.
const (
	FormatText       = "text"
	FormatStreamJSON = "stream-json"
)

// Placeholder names accepted inside a fragment's arguments.
const (
	placeholderPrompt      = "prompt"
	placeholderSession     = "session"
	placeholderModel       = "model"
	placeholderLevel       = "level"
	placeholderImage       = "image"
	placeholderSettings    = "settings"
	placeholderSessionName = "sessionName"
)

// knownPlaceholders is every {name} a fragment may substitute.
var knownPlaceholders = []string{
	placeholderPrompt, placeholderSession, placeholderModel, placeholderLevel,
	placeholderImage, placeholderSettings, placeholderSessionName,
}

// knownConditions is every name a fragment's `when` may test. The value
// placeholders test as present when non-empty; yolo and remoteControl test the
// run's own settings.
var knownConditions = append([]string{"yolo", "remoteControl"}, knownPlaceholders...)

// placeholderPattern finds {name} spans inside a template argument.
var placeholderPattern = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)\}`)

// skillsTargetPattern is the shape a skills.sh target id may take. The set of
// ids skills.sh knows grows without glorp, so the shape is checked and the
// value is not: an id glorp has never heard of is the user's to pass on.
var skillsTargetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// namePattern is the shape an agent name may take. It has to survive being
// written in --agent name/model:level, so it excludes the separators that
// syntax uses.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Validate reports the first thing wrong with the definition, naming the field
// it was found in. A definition that does not validate is never registered: a
// silently dropped agent looks exactly like a typo in --agent.
func (d Definition) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf(`field "name" is required`)
	}
	if !namePattern.MatchString(d.Name) {
		return fmt.Errorf(`field "name": %q must be letters, digits, dot, dash, or underscore`, d.Name)
	}
	if strings.TrimSpace(d.Binary) == "" {
		return fmt.Errorf(`field "binary" is required`)
	}
	if len(d.Args.Run) == 0 {
		return fmt.Errorf(`field "args.run" is required`)
	}
	if len(d.Args.Resume) == 0 {
		return fmt.Errorf(`field "args.resume" is required`)
	}
	for mode, fragments := range map[string][]Fragment{
		"args.run": d.Args.Run, "args.resume": d.Args.Resume, "args.vision": d.Args.Vision,
	} {
		if err := validateFragments(mode, fragments); err != nil {
			return err
		}
	}
	switch d.Session.Assign {
	case AssignGlorp, AssignNone:
		if d.Session.Capture != "" {
			return fmt.Errorf(`field "session.capture" is only meaningful when "session.assign" is %q`, AssignCapture)
		}
	case AssignCapture:
		if strings.TrimSpace(d.Session.Capture) == "" {
			return fmt.Errorf(`field "session.capture" is required when "session.assign" is %q`, AssignCapture)
		}
		expr, err := regexp.Compile(d.Session.Capture)
		if err != nil {
			return fmt.Errorf(`field "session.capture": %w`, err)
		}
		if expr.NumSubexp() < 1 {
			return fmt.Errorf(`field "session.capture": %q has no capture group for the session ID`, d.Session.Capture)
		}
	default:
		return fmt.Errorf(`field "session.assign": %q must be %q, %q, or %q`, d.Session.Assign, AssignGlorp, AssignCapture, AssignNone)
	}
	switch d.Output.Format {
	case FormatText, FormatStreamJSON:
	default:
		return fmt.Errorf(`field "output.format": %q must be %q or %q`, d.Output.Format, FormatText, FormatStreamJSON)
	}
	for _, level := range d.Levels {
		if strings.TrimSpace(level) == "" {
			return fmt.Errorf(`field "levels" cannot contain an empty value`)
		}
	}
	for _, model := range d.Models {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf(`field "models" cannot contain an empty value`)
		}
	}
	if err := d.Quota.validate(); err != nil {
		return err
	}
	if target := d.Skills.Target; target != "" && !skillsTargetPattern.MatchString(target) {
		return fmt.Errorf(`field "skills.target": %q must be a skills.sh target id: lowercase letters, digits, and dashes`, target)
	}
	for key := range d.Env {
		if strings.TrimSpace(key) == "" || strings.Contains(key, "=") {
			return fmt.Errorf(`field "env": %q is not a usable variable name`, key)
		}
	}
	return nil
}

func validateFragments(mode string, fragments []Fragment) error {
	for i, fragment := range fragments {
		if condition := strings.TrimPrefix(fragment.When, "!"); condition != "" && !contains(knownConditions, condition) {
			return fmt.Errorf(`field %q[%d].when: unknown condition %q; known conditions are %s`, mode, i, fragment.When, strings.Join(sorted(knownConditions), ", "))
		}
		if len(fragment.Args) == 0 {
			return fmt.Errorf(`field %q[%d].args cannot be empty`, mode, i)
		}
		for _, arg := range fragment.Args {
			for _, match := range placeholderPattern.FindAllStringSubmatch(arg, -1) {
				if !contains(knownPlaceholders, match[1]) {
					return fmt.Errorf(`field %q[%d].args: unknown placeholder %s; known placeholders are {%s}`, mode, i, match[0], strings.Join(sorted(knownPlaceholders), "}, {"))
				}
			}
		}
	}
	return nil
}

// SkillsTarget returns the skills.sh target id skills are installed for on
// this agent's behalf, empty when the definition names none.
func (d Definition) SkillsTarget() string { return d.Skills.Target }

// AcceptsLevel and AcceptsModel report whether the definition's allow-lists
// admit a value. An empty allow-list admits anything.
func (d Definition) AcceptsLevel(level string) bool { return admits(d.Levels, level) }

// AcceptsModel reports whether the definition's model allow-list admits a model.
func (d Definition) AcceptsModel(model string) bool { return admits(d.Models, model) }

func admits(allowed []string, value string) bool {
	return len(allowed) == 0 || contains(allowed, value)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// JoinOr renders a list the way an error message wants to read it: "low,
// medium, or high".
func JoinOr(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	case 2:
		return values[0] + " or " + values[1]
	}
	return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
}
