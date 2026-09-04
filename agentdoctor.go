package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/lsegal/glorp/agents"
	"github.com/lsegal/glorp/process"
)

// Status markers the report prefixes each agent with, in the shape a doctor
// command is read at a glance: ready, installed but incomplete, and missing.
// They are ASCII because the report is printed to whatever console the user
// happens to be on, including a Windows one that would render check marks as
// mojibake.
const (
	statusReady   = "[ok]"
	statusWarn    = "[!]"
	statusMissing = "[x]"

	// statusWidth pads the markers to one column, so the agent names line up
	// however many of each the registry produces.
	statusWidth = 4
)

// Text the report prints where a probe could not answer. "unknown" is a real
// answer here -- an agent that declares no sign-in probe is not signed out --
// so it is never rendered as a failure.
const (
	doctorUnknown   = "unknown"
	doctorSignedIn  = "signed in"
	doctorSignedOut = "signed out"
)

// agentReport is what the doctor report knows about one agent after probing.
type agentReport struct {
	name string
	// binary is the executable the definition names, and path is where it was
	// found; an empty path is an agent whose CLI is not installed.
	binary string
	path   string
	// version is the first line of `binary --version`, and versionNote says
	// why it is a problem when the definition declares a minimum it misses.
	version     string
	versionNote string
	// auth is the sign-in state, quota the agent's own quota reading, and
	// models the fully qualified agent/model names --agent accepts.
	auth string
	// quota is the reading, and tracksQuota says the definition names a quota
	// source at all. The two apart are what tell an agent that reports no
	// quota by design from one whose reader could not answer, which look the
	// same on the line and mean opposite things.
	quota       string
	tracksQuota bool
	models      []string
	modelNote   string
}

// installed reports whether the agent's CLI was found on PATH.
func (r agentReport) installed() bool { return r.path != "" }

// status is the marker the agent's line is prefixed with: missing when the CLI
// is not installed, a warning when it is installed but something about it needs
// attention, and ready otherwise.
func (r agentReport) status() string {
	switch {
	case !r.installed():
		return statusMissing
	case r.versionNote != "" || r.auth == doctorSignedOut:
		return statusWarn
	default:
		return statusReady
	}
}

// agentDoctor probes the registry. Every process it runs is behind a function
// field so the tests can answer for a machine with no agent CLI installed at
// all, which is every machine CI runs on.
type agentDoctor struct {
	registry *agents.Registry
	// lookPath resolves an executable, version reads its reported version,
	// run executes one probe and returns its output and whether it succeeded,
	// and quotaFor reads one agent's quota text.
	lookPath func(string) (string, error)
	version  func(ctx context.Context, binary string) ([]byte, error)
	run      func(ctx context.Context, argv []string, spec agents.Doctor) ([]byte, bool)
	quotaFor func(ctx context.Context, name, binary string) string
}

// newAgentDoctor builds the doctor that talks to the real machine.
func newAgentDoctor(registry *agents.Registry) *agentDoctor {
	return &agentDoctor{
		registry: registry,
		lookPath: exec.LookPath,
		version:  func(ctx context.Context, binary string) ([]byte, error) { return agentVersionCommand(ctx, binary) },
		run:      runDoctorProbe,
		quotaFor: readAgentQuota,
	}
}

// runDoctorProbe runs one probe under the definition's timeout, reporting its
// combined output and whether it exited zero. A probe that fails is not an
// error the report stops for: it is the answer.
func runDoctorProbe(ctx context.Context, argv []string, spec agents.Doctor) ([]byte, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, spec.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// A probe is told nothing, so a CLI that would prompt reads EOF and gives
	// up instead of waiting on a terminal the report does not have.
	cmd.Stdin = strings.NewReader("")
	cmd.WaitDelay = quotaCommandWaitDelay
	out, err := process.CombinedOutput(cmd)
	return out, err == nil
}

// readAgentQuota reads one agent's quota through the same readers the watch
// status bar uses, so the report never grows a second opinion about quota.
func readAgentQuota(ctx context.Context, name, binary string) string {
	readers := namedQuotaReaders(agentRegistry(), []string{name}, func(string) string { return binary })
	if len(readers) == 0 {
		return ""
	}
	return strings.TrimSpace(readers[0].read(ctx))
}

// Report probes every registered agent and returns one entry each, in the
// registry's sorted order. The agents are probed concurrently: each one costs
// a handful of subprocesses, and a report that ran them one CLI at a time
// would take as long as the slowest of them multiplied by the registry.
func (d *agentDoctor) Report(ctx context.Context) []agentReport {
	names := d.registry.Names()
	reports := make([]agentReport, len(names))
	var wait sync.WaitGroup
	for i, name := range names {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reports[i] = d.reportAgent(ctx, name)
		}()
	}
	wait.Wait()
	return reports
}

// reportAgent probes one agent. Nothing is run when its CLI is not installed:
// every remaining question is about that binary, and the answers would all be
// the same failure.
func (d *agentDoctor) reportAgent(ctx context.Context, name string) agentReport {
	report := agentReport{name: name, auth: doctorUnknown}
	definition, ok := d.registry.Lookup(name)
	if !ok {
		return report
	}
	report.binary = definition.Binary
	path, err := d.lookPath(definition.Binary)
	if err != nil {
		report.models, report.modelNote = declaredModels(definition)
		return report
	}
	report.path = path
	report.version, report.versionNote = d.reportVersion(ctx, definition, path)
	report.tracksQuota = definition.Quota.ReaderName() != agents.QuotaNone
	report.quota = d.quotaFor(ctx, name, path)
	report.auth = d.reportAuth(ctx, definition, path, report.quota)
	report.models, report.modelNote = d.reportModels(ctx, definition, path)
	return report
}

// reportVersion reads the CLI's version and says whether it clears the
// definition's declared minimum. An unreadable version is reported as unknown
// rather than as too old, matching how a dispatch treats it.
func (d *agentDoctor) reportVersion(ctx context.Context, definition agents.Definition, binary string) (version, note string) {
	out, err := d.version(ctx, binary)
	reported := firstLine(strings.TrimSpace(string(out)))
	if err != nil || reported == "" {
		return "", ""
	}
	if strings.TrimSpace(definition.MinVersion) == "" {
		return reported, ""
	}
	supported, comparable := definition.VersionSupported(strings.TrimSpace(string(out)))
	switch {
	case !comparable:
		return reported, fmt.Sprintf("could not be compared with the required %s or newer", definition.MinVersion)
	case !supported:
		return reported, fmt.Sprintf("older than the required %s", definition.MinVersion)
	}
	return reported, ""
}

// reportAuth resolves the sign-in state. A definition's own probe decides it;
// failing that, a quota reading is proof enough, because every quota reader
// glorp has asks the CLI something only a signed-in account can answer. An
// agent with neither is reported as unknown rather than guessed at.
func (d *agentDoctor) reportAuth(ctx context.Context, definition agents.Definition, binary, quota string) string {
	if argv := definition.Doctor.AuthArgv(binary); len(argv) > 0 {
		out, ok := d.run(ctx, argv, definition.Doctor)
		if ok && definition.Doctor.ReportsSignedIn(string(out)) {
			return doctorSignedIn
		}
		return doctorSignedOut
	}
	if strings.TrimSpace(quota) != "" {
		return doctorSignedIn
	}
	return doctorUnknown
}

// reportModels lists the models the agent accepts, preferring what its CLI
// says over what the definition declares: a provider-agnostic CLI's model list
// is whatever it is configured with today, which no static allow-list can
// keep up with.
func (d *agentDoctor) reportModels(ctx context.Context, definition agents.Definition, binary string) ([]string, string) {
	argv := definition.Doctor.ModelsArgv(binary)
	if len(argv) == 0 {
		return declaredModels(definition)
	}
	out, ok := d.run(ctx, argv, definition.Doctor)
	if !ok {
		models, note := declaredModels(definition)
		if len(models) == 0 {
			note = "could not be listed by " + strings.Join(argv, " ")
		}
		return models, note
	}
	models := definition.Doctor.ModelsFrom(string(out))
	if len(models) == 0 {
		return declaredModels(definition)
	}
	return qualify(definition.Name, models), ""
}

// declaredModels is what the definition alone can say about an agent's models:
// its allow-list when it names one, and otherwise a note, because "the
// definition validates any model" and "the agent takes no model" are different
// answers and neither is a list.
func declaredModels(definition agents.Definition) ([]string, string) {
	switch {
	case definition.Models.AcceptsNothing():
		return nil, "not accepted by this agent"
	case definition.Models.Declared():
		return qualify(definition.Name, definition.Models.Values()), ""
	}
	return nil, "any model the CLI accepts"
}

// qualify renders models as the agent/model names --agent takes, which is the
// whole point of listing them.
func qualify(name string, models []string) []string {
	qualified := make([]string, 0, len(models))
	for _, model := range models {
		qualified = append(qualified, name+"/"+model)
	}
	return qualified
}

// doctorFieldWidth is the column the report's field values line up in.
const doctorFieldWidth = 8

// writeAgentReports renders the report. It is a plain block per agent rather
// than a table: the model list is the longest thing on the page and the one
// people are here to copy, so it gets its own lines instead of being wrapped
// into a cell.
func writeAgentReports(out io.Writer, reports []agentReport) {
	for i, report := range reports {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%-*s %s\n", statusWidth, report.status(), report.name)
		writeAgentField(out, "binary", describeBinary(report))
		if !report.installed() {
			continue
		}
		writeAgentField(out, "auth", report.auth)
		writeAgentField(out, "quota", describeQuota(report))
		writeAgentModels(out, report)
	}
}

// describeQuota reads the agent's quota line. An agent that names no quota
// source is not tracked, which is the deliberate default; one that names a
// source and could not be read is unavailable, which is a problem worth
// telling apart from the default.
func describeQuota(report agentReport) string {
	if text := strings.TrimSpace(report.quota); text != "" {
		return text
	}
	if report.tracksQuota {
		return "unavailable"
	}
	return "not tracked"
}

// describeBinary says where the CLI is, or that it is not installed, with the
// version and any complaint about it on the same line.
func describeBinary(report agentReport) string {
	if !report.installed() {
		return fmt.Sprintf("%s (not installed)", orDefault(report.binary, doctorUnknown))
	}
	text := report.path
	if report.version != "" {
		text += ", version " + report.version
	}
	if report.versionNote != "" {
		text += " (" + report.versionNote + ")"
	}
	return text
}

// writeAgentModels prints the models one per line, so every name can be copied
// straight into --agent, with the count on the header line for the agents that
// front dozens of them.
func writeAgentModels(out io.Writer, report agentReport) {
	if len(report.models) == 0 {
		writeAgentField(out, "models", orDefault(report.modelNote, doctorUnknown))
		return
	}
	writeAgentField(out, "models", fmt.Sprintf("%d available", len(report.models)))
	for _, model := range report.models {
		fmt.Fprintf(out, "  %-*s  %s\n", doctorFieldWidth, "", model)
	}
}

func writeAgentField(out io.Writer, name, value string) {
	fmt.Fprintf(out, "  %-*s  %s\n", doctorFieldWidth, name, value)
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
