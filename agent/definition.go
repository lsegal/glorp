// Package agent describes the coding-agent CLIs glorp dispatches work to as
// data rather than as code. Everything glorp has to know to talk to an agent
// -- the executable, the argv for each mode it invokes, the environment, how a
// session ID is established, how its output is decoded -- lives in a
// Definition, so supporting another CLI is a JSON blob instead of another
// branch in six call sites (issue #487).
package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Mode names an invocation shape glorp builds argv for. Each mode has its own
// template because the argv differ structurally, not just by a flag: a resume
// names a session and takes no model, and a vision call is one shot.
const (
	// ModeRun is a fresh dispatch of an issue.
	ModeRun = "run"
	// ModeResume continues an existing session.
	ModeResume = "resume"
	// ModeVision is the one-shot screenshot read browser mode falls back to.
	ModeVision = "vision"
)

// modes lists every mode a definition may declare, for validation messages.
var modes = []string{ModeRun, ModeResume, ModeVision}

// Output formats a definition can ask glorp to decode the agent's stdout as.
const (
	// OutputText passes the agent's output through unchanged.
	OutputText = "text"
	// OutputClaudeStreamJSON decodes Claude's --output-format stream-json
	// events into readable lines.
	OutputClaudeStreamJSON = "claude-stream-json"
)

var outputFormats = []string{OutputText, OutputClaudeStreamJSON}

// Placeholder names a value a template fragment can interpolate with ${name}
// or test for with "when". Keeping the set closed means a typo in a definition
// is a startup error naming the field rather than an empty argument the agent
// silently misreads.
const (
	PlaceholderPrompt                = "prompt"
	PlaceholderSession               = "session"
	PlaceholderModel                 = "model"
	PlaceholderLevel                 = "level"
	PlaceholderImage                 = "image"
	PlaceholderRemoteControlSettings = "remoteControlSettings"
	PlaceholderRemoteControlName     = "remoteControlName"
	// ConditionYolo and ConditionRemoteControl are booleans read off the run
	// rather than text, so they are usable in "when" but not in ${}.
	ConditionYolo          = "yolo"
	ConditionRemoteControl = "remoteControl"
)

var placeholders = []string{
	PlaceholderPrompt, PlaceholderSession, PlaceholderModel, PlaceholderLevel,
	PlaceholderImage, PlaceholderRemoteControlSettings, PlaceholderRemoteControlName,
}

var conditions = append(append([]string{}, placeholders...), ConditionYolo, ConditionRemoteControl)

// Definition is the whole of what glorp knows about one agent CLI.
type Definition struct {
	// Name is the agent name used in --agent name/model:level.
	Name string `json:"name"`
	// Binary is the executable to run when no per-agent binary flag overrides
	// it.
	Binary string `json:"binary"`
	// Args holds the argv template for each mode glorp invokes.
	Args ArgTemplates `json:"args"`
	// Env adds environment variables to the agent's child process, on top of
	// glorp's own environment. Set it to null in a config override to drop
	// every variable a built-in definition contributes.
	Env map[string]string `json:"env"`
	// Session describes how a session ID is established and what happens when
	// resuming one fails.
	Session SessionConfig `json:"session"`
	// Levels and Models optionally restrict what --agent accepts, and are
	// quoted back in the error when it does not. Empty means anything.
	Levels []string `json:"levels"`
	Models []string `json:"models"`
	// Output selects how the agent's stdout is decoded for the dashboard.
	Output OutputConfig `json:"output"`
	// Quota names the built-in quota reader for this agent, if any. Agents
	// without one report their quota as untracked.
	Quota QuotaConfig `json:"quota"`

	// sessionPattern is the compiled Session.CapturePattern, built once at
	// validation so every run does not recompile it.
	sessionPattern *regexp.Regexp
}

// ArgTemplates holds one fragment list per mode.
type ArgTemplates struct {
	Run    []Fragment `json:"run"`
	Resume []Fragment `json:"resume"`
	Vision []Fragment `json:"vision"`
}

// Fragment is one conditionally included run of argv entries. This is a
// deliberately small template model rather than an expression language: a
// fragment contributes its Args verbatim, with ${name} placeholders
// substituted, and is dropped entirely when its When condition does not hold.
type Fragment struct {
	// Args are the argv entries this fragment contributes. Each may contain
	// ${placeholder} references, which are substituted within the entry so
	// "model_reasoning_effort=${level}" renders as one argument.
	Args []string `json:"args"`
	// When names the value that must be set for the fragment to be included:
	// a placeholder that must be non-empty, or "yolo"/"remoteControl" that
	// must be true. Prefix it with "!" to require the opposite, which is how
	// Claude's non-yolo "--permission-mode auto" is expressed. An empty When
	// always includes the fragment.
	When string `json:"when,omitempty"`
}

// SessionConfig says where a session ID comes from.
type SessionConfig struct {
	// Assign has glorp generate the session ID up front and hand it to the
	// agent through the run template's ${session} placeholder, as Claude's
	// --session-id takes. When false the agent picks its own.
	Assign bool `json:"assign"`
	// CapturePattern reads the ID the agent prints on stdout instead, as
	// Codex's "session id: <uuid>" line does. The first capturing group is
	// the ID.
	CapturePattern string `json:"capturePattern,omitempty"`
	// ClearOnResumeFailure drops the recorded ID before the work is restarted
	// from scratch, which an agent that assigns its own IDs requires: reusing
	// the old one would ask for the session that just proved to be gone.
	ClearOnResumeFailure bool `json:"clearOnResumeFailure"`
}

// OutputConfig selects the stdout decoder. Pluggable decoding beyond the two
// existing values is issue #488; the field is named here so that issue does
// not have to reshape the schema.
type OutputConfig struct {
	Format string `json:"format"`
}

// QuotaConfig names the built-in quota reader for the agent. Registry-driven
// quota readers are issue #489; the field is named here for the same reason.
type QuotaConfig struct {
	Reader string `json:"reader,omitempty"`
}

// Values are the run-specific values a template renders against.
type Values struct {
	Prompt        string
	Session       string
	Model         string
	Level         string
	Image         string
	Yolo          bool
	RemoteControl bool
	// RemoteControlSettings and RemoteControlName carry the two arguments
	// Claude's Remote Control opt-in passes. They are values rather than
	// literals in the template so the caller decides what a run is named.
	RemoteControlSettings string
	RemoteControlName     string
}

func (v Values) text(name string) (string, bool) {
	switch name {
	case PlaceholderPrompt:
		return v.Prompt, true
	case PlaceholderSession:
		return v.Session, true
	case PlaceholderModel:
		return v.Model, true
	case PlaceholderLevel:
		return v.Level, true
	case PlaceholderImage:
		return v.Image, true
	case PlaceholderRemoteControlSettings:
		return v.RemoteControlSettings, true
	case PlaceholderRemoteControlName:
		return v.RemoteControlName, true
	}
	return "", false
}

func (v Values) truthy(name string) bool {
	switch name {
	case ConditionYolo:
		return v.Yolo
	case ConditionRemoteControl:
		return v.RemoteControl
	}
	text, _ := v.text(name)
	return text != ""
}

// placeholderPattern matches a ${name} reference inside an argv entry.
var placeholderPattern = regexp.MustCompile(`\$\{([A-Za-z][A-Za-z0-9_]*)\}`)

// fragments returns the template for a mode.
func (d *Definition) fragments(mode string) []Fragment {
	switch mode {
	case ModeRun:
		return d.Args.Run
	case ModeResume:
		return d.Args.Resume
	case ModeVision:
		return d.Args.Vision
	}
	return nil
}

// RenderArgs builds the argv for a mode. A definition that declares no
// template for the mode renders nothing, which is how an agent that cannot be
// asked for a one-shot vision read reports that it has no such invocation.
func (d *Definition) RenderArgs(mode string, values Values) []string {
	fragments := d.fragments(mode)
	if len(fragments) == 0 {
		return nil
	}
	args := make([]string, 0, len(fragments)+4)
	for _, fragment := range fragments {
		if !fragment.included(values) {
			continue
		}
		for _, arg := range fragment.Args {
			args = append(args, substitute(arg, values))
		}
	}
	return args
}

func (f Fragment) included(values Values) bool {
	condition := f.When
	if condition == "" {
		return true
	}
	negated := strings.HasPrefix(condition, "!")
	if negated {
		condition = condition[1:]
	}
	return values.truthy(condition) != negated
}

func substitute(arg string, values Values) string {
	return placeholderPattern.ReplaceAllStringFunc(arg, func(match string) string {
		name := match[2 : len(match)-1]
		text, _ := values.text(name)
		return text
	})
}

// SessionPattern returns the compiled stdout capture pattern, or nil when the
// agent does not report its session ID that way.
func (d *Definition) SessionPattern() *regexp.Regexp { return d.sessionPattern }

// Validate checks a definition well enough that a mistake in one is reported
// at startup naming the offending field, rather than as an agent invocation
// that silently does the wrong thing. source names where the definition came
// from so the message points at the file to fix.
func (d *Definition) Validate(source string) error {
	fail := func(field, format string, args ...interface{}) error {
		return fmt.Errorf("%s: agent %q: field %q: %s", source, d.Name, field, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("%s: agent definition has an empty %q field", source, "name")
	}
	if strings.ContainsAny(d.Name, "/: \t") {
		return fail("name", "an agent name cannot contain a slash, a colon, or whitespace, because --agent parses those as the model and level separators")
	}
	if strings.TrimSpace(d.Binary) == "" {
		return fail("binary", "an agent needs a default executable to run")
	}
	if len(d.Args.Run) == 0 {
		return fail("args.run", "an agent needs argv for a fresh run")
	}
	for _, mode := range modes {
		for i, fragment := range d.fragments(mode) {
			if err := fragment.validate(source, d.Name, mode, i); err != nil {
				return err
			}
		}
	}
	if d.Session.CapturePattern != "" {
		pattern, err := regexp.Compile(d.Session.CapturePattern)
		if err != nil {
			return fail("session.capturePattern", "%v", err)
		}
		if pattern.NumSubexp() < 1 {
			return fail("session.capturePattern", "needs a capturing group around the session ID")
		}
		d.sessionPattern = pattern
	} else {
		d.sessionPattern = nil
	}
	if !d.Session.Assign && d.Session.CapturePattern == "" {
		return fail("session", "set \"assign\" to have glorp assign the session ID, or \"capturePattern\" to read the one the agent prints")
	}
	if d.Session.Assign && d.Session.CapturePattern != "" {
		return fail("session", "\"assign\" and \"capturePattern\" are mutually exclusive: a session ID is either given to the agent or read back from it")
	}
	if d.Output.Format == "" {
		d.Output.Format = OutputText
	}
	if !contains(outputFormats, d.Output.Format) {
		return fail("output.format", "unknown output format %q; known formats are %s", d.Output.Format, strings.Join(outputFormats, ", "))
	}
	for _, level := range d.Levels {
		if strings.TrimSpace(level) == "" {
			return fail("levels", "an allowed level cannot be empty")
		}
	}
	for _, model := range d.Models {
		if strings.TrimSpace(model) == "" {
			return fail("models", "an allowed model cannot be empty")
		}
	}
	return nil
}

func (f Fragment) validate(source, name, mode string, index int) error {
	field := fmt.Sprintf("args.%s[%d]", mode, index)
	fail := func(format string, args ...interface{}) error {
		return fmt.Errorf("%s: agent %q: field %q: %s", source, name, field, fmt.Sprintf(format, args...))
	}
	if len(f.Args) == 0 {
		return fail("an argv fragment contributes no arguments")
	}
	for _, arg := range f.Args {
		for _, match := range placeholderPattern.FindAllStringSubmatch(arg, -1) {
			if !contains(placeholders, match[1]) {
				return fail("unknown placeholder %q; known placeholders are %s", match[1], strings.Join(sorted(placeholders), ", "))
			}
		}
	}
	condition := strings.TrimPrefix(f.When, "!")
	if condition != "" && !contains(conditions, condition) {
		return fail("unknown condition %q; known conditions are %s", condition, strings.Join(sorted(conditions), ", "))
	}
	return nil
}

// AllowsLevel and AllowsModel report whether an --agent value is acceptable.
// An empty allow-list accepts anything, so a definition only has to name its
// levels or models when it wants glorp to reject the others up front.
func (d *Definition) AllowsLevel(level string) bool {
	return level == "" || len(d.Levels) == 0 || contains(d.Levels, level)
}

func (d *Definition) AllowsModel(model string) bool {
	return model == "" || len(d.Models) == 0 || contains(d.Models, model)
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
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
