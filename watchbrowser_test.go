package main

import (
	"flag"
	"io"
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

// The screenshot fallback is off unless it is asked for, and it only means
// anything in browser mode.
func TestResolveBrowserWatchVision(t *testing.T) {
	flags := parseWatchFlags(t, "--browser", "owner/repo")
	options, _, _, err := resolveBrowserWatch(flags, 30*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	if options.Vision {
		t.Fatal("the screenshot fallback is on without -browser-vision")
	}

	flags = parseWatchFlags(t, "--browser", "--browser-vision", "owner/repo")
	options, _, _, err = resolveBrowserWatch(flags, 30*time.Second, false)
	if err != nil {
		t.Fatalf("resolveBrowserWatch: %v", err)
	}
	if !options.Vision {
		t.Fatal("-browser-vision did not enable the fallback")
	}
}

func TestResolveBrowserWatchRejectsVisionWithoutBrowserMode(t *testing.T) {
	flags := parseWatchFlags(t, "--browser-vision", "owner/repo")
	if _, _, _, err := resolveBrowserWatch(flags, 30*time.Second, false); err == nil {
		t.Fatal("expected -browser-vision without -browser to be rejected")
	}
}

// browserPageReaders unwraps the sign-in guard browser mode's issue source is
// wrapped in and returns the page readers underneath, so the wiring tests keep
// asserting on the readers rather than on the guard (issue #379).
func browserPageReaders(t *testing.T, source IssueSource) browserWatchIssues {
	t.Helper()
	guard, ok := source.(browserSignInGuard)
	if !ok {
		t.Fatalf("Issues = %T, want browserSignInGuard", source)
	}
	if guard.recovery == nil {
		t.Fatal("the issue source has no sign-in recovery, so a signed-out profile is never recovered")
	}
	issues, ok := guard.source.(browserWatchIssues)
	if !ok {
		t.Fatalf("guarded source = %T, want browserWatchIssues", guard.source)
	}
	return issues
}

// The acceptance test for browser mode's wiring: with -browser the sources
// Glorp actually reads through must be the browser-backed ones, not merely a
// parsed flag. Issues goes to the page readers and the push-mode project probe
// goes to the board, while everything that still needs the API stays on GHCLI.
func TestApplyBrowserSourcesInjectsBrowserReaders(t *testing.T) {
	gh := GHCLI{Binary: "gh", Filter: "is:issue state:open", AllIssues: true}
	w := &Glorp{Issues: gh, Discussions: gh, Status: gh, Comments: gh, Projects: gh, Out: io.Discard}
	applyBrowserSources(w, &Browser{}, browserWatchOptions{Enabled: true}, gh)

	issues := browserPageReaders(t, w.Issues)
	repos, ok := issues.Repos.(*browserIssueSource)
	if !ok {
		t.Fatalf("Issues.Repos = %T, want *browserIssueSource", issues.Repos)
	}
	if repos.filter != gh.Filter || !repos.allIssues {
		t.Fatalf("issue source filter = %q, allIssues = %v; want the run's -filter and -all-issues", repos.filter, repos.allIssues)
	}
	if repos.pageFor == nil {
		t.Fatal("issue source has no tab opener, so it cannot read a page")
	}
	if repos.hydrate == nil || repos.handled == nil {
		t.Fatal("issue source cannot hydrate dispatch metadata, so candidates would reach dispatch without a body")
	}
	// The screenshot fallback is opt-in: -browser alone must not attach it.
	if repos.vision != nil {
		t.Fatal("the screenshot fallback is attached without -browser-vision")
	}
	if issues.Board == nil {
		t.Fatal("Issues.Board is nil, so project targets have no reader")
	}
	if issues.Board.Filter != gh.Filter || !issues.Board.AllIssues {
		t.Fatalf("board filter = %q, allIssues = %v; want the run's -filter and -all-issues", issues.Board.Filter, issues.Board.AllIssues)
	}
	if issues.Board.Page == nil {
		t.Fatal("board has no tab opener, so it cannot read a board")
	}
	// One board, shared: the issue source and the ProjectState probe read the
	// same target, so a second reader would mean a second tab on one board.
	if w.Projects != issues.Board {
		t.Fatalf("Projects = %v, want the same board the issue source reads (%v)", w.Projects, issues.Board)
	}
	// Discussions has no REST API to trade away, and comments and status
	// writes have no page affordance, so they stay on gh.
	if _, ok := w.Discussions.(GHCLI); !ok {
		t.Fatalf("Discussions = %T, want GHCLI", w.Discussions)
	}
	if _, ok := w.Comments.(GHCLI); !ok {
		t.Fatalf("Comments = %T, want GHCLI", w.Comments)
	}
	if _, ok := w.Status.(GHCLI); !ok {
		t.Fatalf("Status = %T, want GHCLI", w.Status)
	}
}

// -browser-vision reaches the injected issue source, rather than stopping at
// the resolved options.
func TestApplyBrowserSourcesAttachesVisionFallback(t *testing.T) {
	gh := GHCLI{Binary: "gh"}
	w := &Glorp{Issues: gh, Projects: gh, Out: io.Discard}
	applyBrowserSources(w, &Browser{}, browserWatchOptions{Enabled: true, Vision: true}, gh)

	issues := browserPageReaders(t, w.Issues)
	repos, ok := issues.Repos.(*browserIssueSource)
	if !ok {
		t.Fatalf("Issues.Repos = %T, want *browserIssueSource", issues.Repos)
	}
	if repos.vision == nil {
		t.Fatal("-browser-vision did not reach the injected issue source")
	}
}

// Without -browser runWatch passes no browser, and nothing about the run may
// change: every source stays the API-backed one it was built with.
func TestApplyBrowserSourcesWithoutBrowserKeepsAPISources(t *testing.T) {
	gh := GHCLI{Binary: "gh"}
	w := &Glorp{Issues: gh, Discussions: gh, Status: gh, Comments: gh, Projects: gh, Out: io.Discard}
	applyBrowserSources(w, nil, browserWatchOptions{}, gh)

	for name, source := range map[string]interface{}{
		"Issues":      w.Issues,
		"Discussions": w.Discussions,
		"Status":      w.Status,
		"Comments":    w.Comments,
		"Projects":    w.Projects,
	} {
		if _, ok := source.(GHCLI); !ok {
			t.Fatalf("%s = %T without -browser, want GHCLI", name, source)
		}
	}
}
