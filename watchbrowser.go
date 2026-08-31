package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// browserWatchOptions is the resolved `-browser` configuration for a watch run:
// whether browser mode was asked for, and the overrides that decide which
// browser is driven and where its profile lives.
type browserWatchOptions struct {
	Enabled bool
	Binary  string
	Profile string
	// Vision enables the bounded screenshot-to-agent fallback for pages the
	// DOM extractor stops recognising. It is off unless -browser-vision is
	// passed, and it changes nothing on the success path.
	Vision bool
	// Headed drives a visible browser instead of a headless one, so the pages
	// glorp reads can be watched while it reads them. It is a debugging aid
	// (-no-headless, issue #428): a headless browser gives a failing read
	// nothing to look at but the CDP traffic.
	Headed bool
}

// config converts the flag values into the launcher's configuration.
func (o browserWatchOptions) config() browserConfig {
	return browserConfig{Binary: o.Binary, Profile: o.Profile, Headed: o.Headed}
}

// noHeadlessEnvironmentCheck refuses -no-headless where no window could ever
// appear, instead of launching a browser that fails to start or is never seen.
// It is a variable so tests can exercise both answers without depending on the
// display server the test runner happens to have.
var noHeadlessEnvironmentCheck = func() error {
	if displayServerAvailable(runtime.GOOS, os.Getenv) {
		return nil
	}
	return fmt.Errorf("-no-headless needs a display server to show the browser window, and neither DISPLAY nor WAYLAND_DISPLAY is set: drop -no-headless to keep watching headlessly, or run glorp from a desktop session")
}

// browserWatchInterval is the poll interval browser mode defaults to. Reading a
// page glorp already has a tab open on is far cheaper than the API path's `gh`
// invocations, so browser mode can poll on a human timescale instead of the
// half-minute the API default is chosen to stay inside rate limits.
const browserWatchInterval = 5 * time.Second

// browserExclusiveFlags are the webhook-transport flags browser mode can never
// honour: it never starts a webhook server and never opens an ngrok tunnel, so
// passing one of these alongside -browser means the run would not do what the
// command line says.
var browserExclusiveFlags = []string{"listen", "webhook-path", "webhook-secret", "ngrok-binary"}

// resolveBrowserWatch reads the browser-mode flags and reports the transport
// settings the run should actually use. Browser mode implies -poll and, unless
// -interval was passed explicitly, shortens the interval; an explicit interval
// still wins. Combining it with a webhook-transport flag is an error rather
// than a silent no-op, so a user who asked for a tunnel is told it cannot
// happen instead of watching browser mode ignore them. -no-headless is the
// debugging escape hatch: it drives the same run through a visible browser, and
// is refused where no window could appear rather than launching one nobody can
// see.
func resolveBrowserWatch(flags *flag.FlagSet, interval time.Duration, poll bool) (browserWatchOptions, time.Duration, bool, error) {
	options := browserWatchOptions{
		Enabled: flagValue[bool](flags, "browser"),
		Binary:  flagValue[string](flags, "browser-binary"),
		Profile: flagValue[string](flags, "browser-profile"),
		Vision:  flagValue[bool](flags, "browser-vision"),
		Headed:  flagValue[bool](flags, "no-headless"),
	}
	explicit := explicitFlags(flags)
	if !options.Enabled {
		if explicit["browser-vision"] {
			return options, interval, poll, fmt.Errorf("-browser-vision only applies to browser mode, so it cannot be used without -browser")
		}
		if explicit["no-headless"] {
			return options, interval, poll, fmt.Errorf("-no-headless only applies to browser mode, so it cannot be used without -browser")
		}
		return options, interval, poll, nil
	}
	var conflicting []string
	for _, name := range browserExclusiveFlags {
		if explicit[name] {
			conflicting = append(conflicting, "-"+name)
		}
	}
	if len(conflicting) > 0 {
		return options, interval, poll, fmt.Errorf("-browser never starts a webhook server or an ngrok tunnel, so it cannot be combined with %s", strings.Join(conflicting, ", "))
	}
	if options.Headed {
		if err := noHeadlessEnvironmentCheck(); err != nil {
			return options, interval, poll, err
		}
	}
	if !explicit["interval"] {
		interval = browserWatchInterval
	}
	return options, interval, true, nil
}

// explicitFlags reports which flags were actually present on the command line,
// as opposed to sitting at their default. flag.Visit walks only the flags that
// were set, which is the only way to tell "-interval 30s" from the identical
// default.
func explicitFlags(flags *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// applyBrowserSources swaps the reads a run does for their browser-backed
// equivalents. Browser mode reads the issue list off GitHub's own issues page
// (issue #377) and a project board off its Projects v2 page (issue #378), both
// through tabs on the one shared browser the run launched.
//
// Only reads move. Discussions has no REST API to trade for a page read,
// comments drive the handoff handshake, and status writes have no stable page
// affordance, so those stay on GHCLI. A nil browser means -browser was not
// passed, and the run is left exactly as the API path built it.
//
// The board is built once and shared: it answers as the issue source for
// project targets and as the push-mode ProjectState probe, and giving each of
// those its own reader would open two tabs on the same board. Dispatch needs
// the body and dependency state neither rendered page carries, so both readers
// hydrate newly seen candidates through gh, sharing one memo so an issue costs
// its REST calls once for the run whichever page found it (issues #381 and
// #395), and the screenshot fallback is attached only when -browser-vision asked for
// it (issue #384). One browserVision is built and shared by the repository
// source and the board, so its per-run cap is a single budget for the run
// rather than one per page kind (issue #393).
func applyBrowserSources(w *Glorp, browser *Browser, options browserWatchOptions, gh GHCLI) {
	if w == nil || browser == nil {
		return
	}
	var vision *browserVision
	if options.Vision {
		runner, _ := w.Runner.(CommandRunner)
		vision = newBrowserVision(runner, w.logf)
	}
	repos := newBrowserIssueSource(browser, gh, w.issueHandled, gh.Filter, gh.AllIssues, vision, w.logf)
	board := newBrowserBoard(browser, gh.Filter, gh.AllIssues, vision, repos.browserHydration)
	// A read that came back signed out is recovered in place: the run stops
	// the headless browser, opens a login window on the same profile, and
	// starts polling again once the sign-in lands (issue #379). The guard
	// wraps the source rather than living inside it because both page readers
	// reach the same conclusion the same way.
	w.Issues = browserSignInGuard{
		source:   browserWatchIssues{Repos: repos, Board: board},
		recovery: newBrowserSignInRecovery(browser, options.config(), w.logf),
	}
	w.Projects = board
}
