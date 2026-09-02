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
// invocations, so browser mode can poll faster than the half-minute the API
// default is chosen to stay inside rate limits. It is not free, though: a tick
// reloads every watched target's page, waits out GitHub's client-side render,
// and re-reads the conversation of every issue an agent is working, none of
// which fits in the five seconds this was first set to (issue #441).
const browserWatchInterval = 20 * time.Second

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

// browserTabIdleFloor is the shortest a tab is left open with nothing reading
// it, so a short poll interval does not close and reopen tabs on work that is
// merely between reads.
const browserTabIdleFloor = time.Minute

// browserTabIdleTimeout is how long a tab may go unread before the run closes
// it. It is measured in poll intervals, because a page glorp still watches is
// re-read once per tick: missing several ticks in a row is what tells a page
// apart from one nothing is reading any more.
func browserTabIdleTimeout(interval time.Duration) time.Duration {
	idle := 3 * interval
	if idle < browserTabIdleFloor {
		idle = browserTabIdleFloor
	}
	return idle
}

// applyBrowserSources swaps the reads a run does for their browser-backed
// equivalents. Browser mode reads the issue list off GitHub's own issues page
// (issue #377) and a project board off its Projects v2 page (issue #378), both
// through tabs on the one shared browser the run launched.
//
// Only reads move. Discussions has no REST API to trade for a page read, and
// posting a comment or moving a status has no page affordance glorp could
// drive, so those stay on GHCLI. A nil browser means -browser was not passed,
// and the run is left exactly as the API path built it.
//
// Comment reads move too (issue #441): the handoff handshake polls the
// conversation of every contested candidate and of every issue an agent is
// working, which was the heaviest API user left in browser mode. Those reads go
// through one shared tab, and a page that could not be read falls back to the
// API rather than reporting a ticket as unclaimed.
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
	// Every page load the run makes goes through one queue, so a tick that
	// reloads many targets staggers its requests rather than firing them all
	// at once (issue #450). It is attached to the browser rather than to any
	// reader because the burst is the sum of what all the readers do.
	browser.SetLoadQueue(newBrowserLoadQueue(w.Interval, w.logf))
	// A tab glorp has stopped reading is closed rather than left open for the
	// rest of the run (issue #461). Every watched target is re-read on every
	// tick, so what ages out is the conversation of an issue nothing is
	// working any more.
	browser.SetTabIdleTimeout(browserTabIdleTimeout(w.Interval), w.logf)
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
		source:   browserWatchIssues{Repos: repos, Board: board, Work: gh},
		recovery: newBrowserSignInRecovery(browser, options.config(), w.logf),
	}
	w.Projects = board
	if w.Comments != nil {
		w.Comments = newBrowserComments(browser, w.Comments, w.logf)
	}
}
