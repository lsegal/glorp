package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lsegal/glorp/agents"
)

// testDoctorRegistry builds a registry of agents with the probe blocks the
// report reads, without touching the built-in definitions: the report has to
// work for an agent declared in a config file too.
func testDoctorRegistry(t *testing.T) *agents.Registry {
	t.Helper()
	base := func(name string) agents.Definition {
		return agents.Definition{
			Name: name, Binary: name,
			Session: agents.Session{Assign: agents.AssignNone},
			Output:  agents.Output{Format: agents.FormatText},
			Args:    agents.Args{Run: []agents.Fragment{{Args: []string{"{prompt}"}}}},
		}
	}
	probed := base("probed")
	probed.Doctor = agents.Doctor{
		Auth:   []string{"{binary}", "login", "status"},
		Models: []string{"{binary}", "models"},
	}
	probed.Quota = agents.Quota{Reader: agents.QuotaClaude}
	declared := base("declared")
	declared.Models = agents.NewAllowList("opus", "sonnet")
	silent := base("silent")
	nomodel := base("nomodel")
	nomodel.Models = agents.NewAllowList()
	old := base("old")
	old.MinVersion = "2.0.0"
	registry, err := agents.NewRegistry(probed, declared, silent, nomodel, old)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

// stubDoctor wires a doctor whose every probe is answered in-process, so the
// report is testable on a machine with no agent CLI installed.
func stubDoctor(t *testing.T, installed map[string]bool, version, auth, models func(string) (string, bool), quota map[string]string) *agentDoctor {
	t.Helper()
	registry := testDoctorRegistry(t)
	return &agentDoctor{
		registry: registry,
		lookPath: func(binary string) (string, error) {
			if !installed[binary] {
				return "", errors.New("not found")
			}
			return "/opt/" + binary, nil
		},
		version: func(_ context.Context, binary string) ([]byte, error) {
			text, ok := version(binary)
			if !ok {
				return nil, errors.New("no version")
			}
			return []byte(text), nil
		},
		run: func(_ context.Context, argv []string, _ agents.Doctor) ([]byte, bool) {
			probe := auth
			if argv[len(argv)-1] == "models" {
				probe = models
			}
			text, ok := probe(argv[0])
			return []byte(text), ok
		},
		quotaFor: func(_ context.Context, name, _ string) string { return quota[name] },
	}
}

// reportFor finds one agent's entry in a report.
func reportFor(t *testing.T, reports []agentReport, name string) agentReport {
	t.Helper()
	for _, report := range reports {
		if report.name == name {
			return report
		}
	}
	t.Fatalf("report has no entry for %q", name)
	return agentReport{}
}

// noProbe answers every probe with a failure, for the agents a test is not
// about.
func noProbe(string) (string, bool) { return "", false }

// TestAgentDoctorReportsEveryRegisteredAgent checks the report covers the whole
// registry in its sorted order, so an agent a config file adds is reported
// beside the built-ins rather than left out of the listing it belongs to.
func TestAgentDoctorReportsEveryRegisteredAgent(t *testing.T) {
	doctor := stubDoctor(t, nil, noProbe, noProbe, noProbe, nil)
	reports := doctor.Report(context.Background())
	if len(reports) != len(doctor.registry.Names()) {
		t.Fatalf("report has %d entries, want %d", len(reports), len(doctor.registry.Names()))
	}
	for i, name := range doctor.registry.Names() {
		if reports[i].name != name {
			t.Fatalf("report[%d] = %q, want %q in registry order", i, reports[i].name, name)
		}
	}
}

// TestAgentDoctorSkipsProbesForAMissingBinary checks nothing is run for an
// agent whose CLI is not installed: every remaining question is about that
// binary, and running them would only produce the same failure five times.
func TestAgentDoctorSkipsProbesForAMissingBinary(t *testing.T) {
	ran := false
	probe := func(string) (string, bool) { ran = true; return "", true }
	doctor := stubDoctor(t, nil, probe, probe, probe, map[string]string{"probed": "week 50% left"})
	doctor.quotaFor = func(context.Context, string, string) string { ran = true; return "" }
	report := reportFor(t, doctor.Report(context.Background()), "probed")
	if ran {
		t.Error("a probe ran for an agent whose binary is not installed")
	}
	if report.installed() || report.status() != statusMissing {
		t.Errorf("status = %q, want %q for a missing binary", report.status(), statusMissing)
	}
	if report.auth != doctorUnknown {
		t.Errorf("auth = %q, want %q for a missing binary", report.auth, doctorUnknown)
	}
}

// TestAgentDoctorReadsTheAuthProbe checks the definition's own probe decides
// the sign-in state, including the CLI that reports a signed-out account on a
// zero exit status.
func TestAgentDoctorReadsTheAuthProbe(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		ok     bool
		want   string
	}{
		{name: "signed in", output: "Logged in using ChatGPT", ok: true, want: doctorSignedIn},
		{name: "probe failed", output: "not authenticated", want: doctorSignedOut},
	} {
		t.Run(test.name, func(t *testing.T) {
			auth := func(string) (string, bool) { return test.output, test.ok }
			doctor := stubDoctor(t, map[string]bool{"probed": true}, noProbe, auth, noProbe, nil)
			if got := reportFor(t, doctor.Report(context.Background()), "probed").auth; got != test.want {
				t.Errorf("auth = %q, want %q", got, test.want)
			}
		})
	}
}

// TestAgentDoctorFallsBackToTheQuotaReadingForAuth checks an agent with no
// sign-in probe of its own is reported as signed in when its quota could be
// read, because every quota reader asks the CLI something only a signed-in
// account can answer, and as unknown when there is no evidence either way.
func TestAgentDoctorFallsBackToTheQuotaReadingForAuth(t *testing.T) {
	installed := map[string]bool{"silent": true}
	withQuota := stubDoctor(t, installed, noProbe, noProbe, noProbe, map[string]string{"silent": "week 40% left"})
	if got := reportFor(t, withQuota.Report(context.Background()), "silent").auth; got != doctorSignedIn {
		t.Errorf("auth = %q, want %q when a quota was read", got, doctorSignedIn)
	}
	without := stubDoctor(t, installed, noProbe, noProbe, noProbe, nil)
	if got := reportFor(t, without.Report(context.Background()), "silent").auth; got != doctorUnknown {
		t.Errorf("auth = %q, want %q with no evidence either way", got, doctorUnknown)
	}
}

// TestAgentDoctorQualifiesProbedModels checks the models a CLI lists are
// rendered as the agent/model names --agent takes, which is the whole reason
// the report lists them.
func TestAgentDoctorQualifiesProbedModels(t *testing.T) {
	models := func(string) (string, bool) { return "openai/gpt-5.6\nanthropic/claude-opus-5\n", true }
	doctor := stubDoctor(t, map[string]bool{"probed": true}, noProbe, noProbe, models, nil)
	report := reportFor(t, doctor.Report(context.Background()), "probed")
	want := []string{"probed/openai/gpt-5.6", "probed/anthropic/claude-opus-5"}
	if strings.Join(report.models, ",") != strings.Join(want, ",") {
		t.Errorf("models = %v, want %v", report.models, want)
	}
}

// TestAgentDoctorFallsBackToDeclaredModels checks the three things a definition
// alone can say about models, which stay distinguishable in the report: an
// allow-list, an agent that takes no model, and one that validates any.
func TestAgentDoctorFallsBackToDeclaredModels(t *testing.T) {
	doctor := stubDoctor(t, map[string]bool{"declared": true, "nomodel": true, "silent": true}, noProbe, noProbe, noProbe, nil)
	reports := doctor.Report(context.Background())
	declared := reportFor(t, reports, "declared")
	if strings.Join(declared.models, ",") != "declared/opus,declared/sonnet" {
		t.Errorf("declared models = %v, want the allow-list qualified", declared.models)
	}
	if got := reportFor(t, reports, "nomodel"); len(got.models) != 0 || !strings.Contains(got.modelNote, "not accepted") {
		t.Errorf("nomodel = %v/%q, want no models and a note", got.models, got.modelNote)
	}
	if got := reportFor(t, reports, "silent"); len(got.models) != 0 || !strings.Contains(got.modelNote, "any model") {
		t.Errorf("silent = %v/%q, want no models and a note", got.models, got.modelNote)
	}
}

// TestAgentDoctorFlagsAnOldBinary checks a CLI below its definition's declared
// minimum is called out rather than reported as ready, which is the whole point
// of declaring the minimum.
func TestAgentDoctorFlagsAnOldBinary(t *testing.T) {
	version := func(string) (string, bool) { return "1.4.0", true }
	doctor := stubDoctor(t, map[string]bool{"old": true}, version, noProbe, noProbe, nil)
	report := reportFor(t, doctor.Report(context.Background()), "old")
	if report.version != "1.4.0" {
		t.Errorf("version = %q, want the reported one", report.version)
	}
	if !strings.Contains(report.versionNote, "2.0.0") || report.status() != statusWarn {
		t.Errorf("versionNote = %q, status = %q, want a warning naming the minimum", report.versionNote, report.status())
	}
}

// TestDescribeQuotaTellsUntrackedFromUnavailable checks the two empty readings
// stay apart on the line: an agent that names no quota source is not tracked by
// design, while one whose reader could not answer is a problem.
func TestDescribeQuotaTellsUntrackedFromUnavailable(t *testing.T) {
	for _, test := range []struct {
		report agentReport
		want   string
	}{
		{report: agentReport{quota: "week 40% left", tracksQuota: true}, want: "week 40% left"},
		{report: agentReport{tracksQuota: true}, want: "unavailable"},
		{report: agentReport{}, want: "not tracked"},
	} {
		if got := describeQuota(test.report); got != test.want {
			t.Errorf("describeQuota(%+v) = %q, want %q", test.report, got, test.want)
		}
	}
}

// TestWriteAgentReportsRendersEveryField checks the rendered report carries
// what the command exists to show, with each model on a line of its own so it
// can be copied straight into --agent.
func TestWriteAgentReportsRendersEveryField(t *testing.T) {
	var out strings.Builder
	writeAgentReports(&out, []agentReport{
		{
			name: "codex", binary: "codex", path: "/opt/codex", version: "1.2.3",
			auth: doctorSignedIn, quota: "week 40% left", tracksQuota: true,
			models: []string{"codex/gpt-5.6", "codex/gpt-5.6-codex"},
		},
		{name: "muse", binary: "muse", modelNote: "any model the CLI accepts"},
	})
	text := out.String()
	for _, want := range []string{
		statusReady + " codex", "/opt/codex, version 1.2.3", "signed in", "week 40% left",
		"2 available", "\n" + strings.Repeat(" ", 12) + "codex/gpt-5.6\n",
		statusMissing + "  muse", "muse (not installed)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report does not contain %q:\n%s", want, text)
		}
	}
	// A missing binary reports nothing beyond that: the remaining fields were
	// never probed, so printing them would be inventing answers.
	muse := text[strings.Index(text, statusMissing):]
	if strings.Contains(muse, "auth") || strings.Contains(muse, "quota") {
		t.Errorf("report prints unprobed fields for a missing binary:\n%s", muse)
	}
}

// TestRunAgentsCommandListingsRunNoProbe checks -names and -skills still print
// the plain listings scripts and both installers parse, rather than the report.
func TestRunAgentsCommandListingsRunNoProbe(t *testing.T) {
	for _, flag := range []string{"-names", "-skills"} {
		out := captureStdout(t, func() {
			if code := runAgentsCommand([]string{flag, "-config", t.TempDir() + "/absent.json"}); code != 0 {
				t.Fatalf("glorp agents %s = %d, want 0", flag, code)
			}
		})
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.Contains(line, " ") {
				t.Fatalf("glorp agents %s printed %q, want one bare value per line", flag, line)
			}
		}
		if flag == "-names" && !strings.Contains(out, "codex\n") {
			t.Fatalf("glorp agents -names = %q, want the built-in names", out)
		}
	}
}

// TestAgentsUsageDocumentsTheReport keeps `glorp help agents` describing what
// the command now prints, so the help is not still promising a bare name list.
func TestAgentsUsageDocumentsTheReport(t *testing.T) {
	cmd, ok := lookupCommand("agents")
	if !ok {
		t.Fatal("there is no agents command")
	}
	for _, want := range []string{"agent/model", "-names", "-skills"} {
		if !strings.Contains(cmd.usage, want) {
			t.Errorf("glorp help agents does not mention %q", want)
		}
	}
	flags := commandFlags("agents")
	if flags == nil {
		t.Fatal(`commandFlags("agents") is nil, so glorp help agents prints no flags`)
	}
	for _, name := range []string{"names", "skills", "config"} {
		if flags.Lookup(name) == nil {
			t.Errorf("glorp agents has no -%s flag", name)
		}
	}
}
