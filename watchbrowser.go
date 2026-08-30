package main

import (
	"flag"
	"fmt"
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
}

// config converts the flag values into the launcher's configuration.
func (o browserWatchOptions) config() browserConfig {
	return browserConfig{Binary: o.Binary, Profile: o.Profile}
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
// happen instead of watching browser mode ignore them.
func resolveBrowserWatch(flags *flag.FlagSet, interval time.Duration, poll bool) (browserWatchOptions, time.Duration, bool, error) {
	options := browserWatchOptions{
		Enabled: flagValue[bool](flags, "browser"),
		Binary:  flagValue[string](flags, "browser-binary"),
		Profile: flagValue[string](flags, "browser-profile"),
	}
	if !options.Enabled {
		return options, interval, poll, nil
	}
	explicit := explicitFlags(flags)
	var conflicting []string
	for _, name := range browserExclusiveFlags {
		if explicit[name] {
			conflicting = append(conflicting, "-"+name)
		}
	}
	if len(conflicting) > 0 {
		return options, interval, poll, fmt.Errorf("-browser never starts a webhook server or an ngrok tunnel, so it cannot be combined with %s", strings.Join(conflicting, ", "))
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
// those its own reader would open two tabs on the same board.
func applyBrowserSources(w *Glorp, browser *Browser, filter string, allIssues bool) {
	if w == nil || browser == nil {
		return
	}
	board := newBrowserBoard(browser, filter, allIssues)
	w.Issues = browserWatchIssues{
		Repos: newBrowserIssueSource(browser, filter, allIssues, w.logf),
		Board: board,
	}
	w.Projects = board
}
