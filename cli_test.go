package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCLIRejectsUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	if code := runCLI([]string{"lsegal/glorp"}, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	output := stderr.String()
	if !strings.Contains(output, `unknown command "lsegal/glorp"`) {
		t.Fatalf("stderr = %q, want an unknown command message", output)
	}
	// The old top-level form is gone, so the error has to point at watch.
	if !strings.Contains(output, "watch") {
		t.Fatalf("stderr = %q, want the command list", output)
	}
}

func TestRunCLIWithoutArgumentsPrintsUsage(t *testing.T) {
	var stderr bytes.Buffer
	if code := runCLI(nil, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	for _, name := range []string{"watch", "ui", "version", "upgrade", "help"} {
		if !strings.Contains(stderr.String(), name) {
			t.Fatalf("usage = %q, want it to list %q", stderr.String(), name)
		}
	}
}

func TestNormalizeCommandMapsLegacyFlags(t *testing.T) {
	cases := map[string]string{
		"--version": "version",
		"-version":  "version",
		"-v":        "version",
		"--help":    "help",
		"-h":        "help",
		"watch":     "watch",
		"ui":        "ui",
		"owner/rep": "owner/rep",
	}
	for input, want := range cases {
		if got := normalizeCommand(input); got != want {
			t.Fatalf("normalizeCommand(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEveryCommandIsRunnable(t *testing.T) {
	for _, name := range []string{"watch", "ui", "version", "upgrade", "help"} {
		cmd, ok := lookupCommand(name)
		if !ok {
			t.Fatalf("command %q is missing", name)
		}
		if cmd.run == nil {
			t.Fatalf("command %q has no run function", name)
		}
		if !strings.Contains(cmd.usage, "glorp "+name) {
			t.Fatalf("command %q usage = %q, want it to show its own invocation", name, cmd.usage)
		}
	}
}

func TestCommandFlagsExposeWatchAndUIDefaults(t *testing.T) {
	watch := commandFlags("watch")
	if watch == nil {
		t.Fatal("watch has no flag set")
	}
	for _, name := range []string{"interval", "poll", "agent", "filter", "web-ui-port", "ui", "concurrency"} {
		if watch.Lookup(name) == nil {
			t.Fatalf("watch flag %q is missing", name)
		}
	}
	ui := commandFlags("ui")
	if ui == nil || ui.Lookup("port") == nil {
		t.Fatal("ui flag set is missing its port flag")
	}
	if commandFlags("version") != nil {
		t.Fatal("version should have no flags")
	}
}

func TestWatchFlagValuesParse(t *testing.T) {
	agents := agentFlag{values: []agentSpec{{Name: "codex"}}}
	filter := filterFlag{values: []string{defaultIssueFilter}}
	flags := watchFlagSet(&agents, &filter)
	if err := flags.Parse([]string{"--poll", "--concurrency", "5", "--interval", "2m", "--agent", "claude/opus", "owner/repo"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := flagValue[bool](flags, "poll"); !got {
		t.Fatal("poll = false, want true")
	}
	if got := flagValue[int](flags, "concurrency"); got != 5 {
		t.Fatalf("concurrency = %d, want 5", got)
	}
	if got := flagValue[string](flags, "state"); got != ".glorp.json" {
		t.Fatalf("state = %q, want the default", got)
	}
	if got := flagValue[int](flags, "missing-flag"); got != 0 {
		t.Fatalf("missing flag = %d, want the zero value", got)
	}
	if got := flags.Args(); len(got) != 1 || got[0] != "owner/repo" {
		t.Fatalf("targets = %v, want [owner/repo]", got)
	}
	if agents.values[0].Name != "claude" || agents.values[0].Model != "opus" {
		t.Fatalf("agents = %v, want claude/opus", agents.values)
	}
}

func TestWatchRemoteControlDefaultsOn(t *testing.T) {
	agents := agentFlag{values: []agentSpec{{Name: "codex"}}}
	filter := filterFlag{values: []string{defaultIssueFilter}}
	flags := watchFlagSet(&agents, &filter)
	if err := flags.Parse([]string{"owner/repo"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !flagValue[bool](flags, "remote-control") {
		t.Fatal("remote-control = false, want the default to be on")
	}

	off := watchFlagSet(&agents, &filter)
	if err := off.Parse([]string{"--remote-control=false", "owner/repo"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flagValue[bool](off, "remote-control") {
		t.Fatal("remote-control = true, want it turned off")
	}
}
