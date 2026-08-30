package main

import (
	"flag"
	"strings"
	"testing"
	"time"
)

// parseWatchFlags parses a watch command line the way runWatch does, so the
// tests see the same explicit-versus-default distinction the resolver relies on.
func parseWatchFlags(t *testing.T, args ...string) *flag.FlagSet {
	t.Helper()
	agents := agentFlag{values: []agentSpec{{Name: "codex"}}}
	filter := filterFlag{values: []string{defaultIssueFilter}}
	flags := watchFlagSet(&agents, &filter)
	flags.Init("watch", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return flags
}

func TestWatchFlagSetHasBrowserFlags(t *testing.T) {
	flags := commandFlags("watch")
	for _, name := range []string{"browser", "browser-profile", "browser-binary"} {
		found := flags.Lookup(name)
		if found == nil {
			t.Fatalf("watch flag %q is missing", name)
		}
		if strings.TrimSpace(found.Usage) == "" {
			t.Fatalf("watch flag %q has no help text", name)
		}
	}
}

func TestResolveBrowserWatchImpliesPollAndFiveSecondInterval(t *testing.T) {
	flags := parseWatchFlags(t, "--browser", "owner/repo")
	options, interval, poll, err := resolveBrowserWatch(flags, 30*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	if !options.Enabled {
		t.Fatal("browser mode is not enabled")
	}
	if !poll {
		t.Fatal("poll = false, want -browser to imply -poll")
	}
	if interval != 5*time.Second {
		t.Fatalf("interval = %s, want 5s", interval)
	}
}

func TestResolveBrowserWatchKeepsExplicitInterval(t *testing.T) {
	flags := parseWatchFlags(t, "--browser", "--interval", "45s", "owner/repo")
	_, interval, _, err := resolveBrowserWatch(flags, 45*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	if interval != 45*time.Second {
		t.Fatalf("interval = %s, want the explicit 45s", interval)
	}
}

// An interval that happens to equal the API default is still explicit, so it
// must survive: the resolver has to read what was passed, not what it equals.
func TestResolveBrowserWatchKeepsExplicitIntervalMatchingTheDefault(t *testing.T) {
	flags := parseWatchFlags(t, "--browser", "--interval", "30s", "owner/repo")
	_, interval, _, err := resolveBrowserWatch(flags, 30*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	if interval != 30*time.Second {
		t.Fatalf("interval = %s, want the explicit 30s", interval)
	}
}

func TestResolveBrowserWatchRejectsWebhookFlags(t *testing.T) {
	for _, testCase := range []struct{ name, arg, value string }{
		{"listen", "--listen", ":8080"},
		{"webhook path", "--webhook-path", "/hook"},
		{"webhook secret", "--webhook-secret", "s3cret"},
		{"ngrok binary", "--ngrok-binary", "/usr/bin/ngrok"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			flags := parseWatchFlags(t, "--browser", testCase.arg, testCase.value, "owner/repo")
			_, _, _, err := resolveBrowserWatch(flags, 30*time.Second, false)
			if err == nil {
				t.Fatalf("expected %s to conflict with -browser", testCase.arg)
			}
			if !strings.Contains(err.Error(), testCase.arg[1:]) {
				t.Fatalf("error %q does not name the conflicting flag", err)
			}
		})
	}
}

func TestResolveBrowserWatchReportsEveryConflictingFlag(t *testing.T) {
	flags := parseWatchFlags(t, "--browser", "--listen", ":8080", "--webhook-secret", "s3cret", "owner/repo")
	_, _, _, err := resolveBrowserWatch(flags, 30*time.Second, false)
	if err == nil {
		t.Fatal("expected a conflict error")
	}
	for _, name := range []string{"-listen", "-webhook-secret"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not mention %s", err, name)
		}
	}
}

// Without -browser the resolver must be inert: the webhook flags stay legal,
// the interval and poll settings pass through, and browser mode stays off so
// nothing in runWatch launches a browser.
func TestResolveBrowserWatchLeavesApiModeUntouched(t *testing.T) {
	flags := parseWatchFlags(t, "--listen", ":8080", "--ngrok-binary", "ngrok", "owner/repo")
	options, interval, poll, err := resolveBrowserWatch(flags, 30*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	if options.Enabled {
		t.Fatal("browser mode is enabled without -browser")
	}
	if interval != 30*time.Second || poll {
		t.Fatalf("interval = %s, poll = %v, want the values passed in", interval, poll)
	}
}

func TestResolveBrowserWatchCarriesBinaryAndProfileOverrides(t *testing.T) {
	flags := parseWatchFlags(t, "--browser", "--browser-binary", "/opt/chromium", "--browser-profile", "/tmp/profile", "owner/repo")
	options, _, _, err := resolveBrowserWatch(flags, 30*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	config := options.config()
	if config.Binary != "/opt/chromium" || config.Profile != "/tmp/profile" {
		t.Fatalf("config = %+v, want the flag overrides", config)
	}
}

func TestWatchHelpDocumentsBrowserMode(t *testing.T) {
	cmd, ok := lookupCommand("watch")
	if !ok {
		t.Fatal("watch command is missing")
	}
	if !strings.Contains(cmd.usage, "-browser") {
		t.Fatal("watch usage does not describe browser mode")
	}
}
