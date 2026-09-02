package browser

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
)

// documentResponse is a main-frame document response as the browser reports it.
func documentResponse(frame cdp.FrameID, status int64) *network.EventResponseReceived {
	return &network.EventResponseReceived{
		FrameID:  frame,
		Type:     network.ResourceTypeDocument,
		Response: &network.Response{Status: status},
	}
}

func TestTabRecordsMainFrameStatus(t *testing.T) {
	tab := &Tab{mainFrame: "main"}
	if got := tab.HTTPStatus(); got != 0 {
		t.Fatalf("status %d before any navigation, want 0", got)
	}
	tab.observe(documentResponse("main", 404))
	if got := tab.HTTPStatus(); got != 404 {
		t.Fatalf("status %d, want 404", got)
	}
}

// TestTabIgnoresSubframeStatus guards the status glorp reads against
// being replaced by whatever an embedded frame on the page was served with.
func TestTabIgnoresSubframeStatus(t *testing.T) {
	tab := &Tab{mainFrame: "main"}
	tab.observe(documentResponse("main", 200))
	tab.observe(documentResponse("embedded", 500))
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want the main frame's 200", got)
	}
}

func TestTabIgnoresNonDocumentResponses(t *testing.T) {
	tab := &Tab{mainFrame: "main"}
	tab.observe(documentResponse("main", 200))
	tab.observe(&network.EventResponseReceived{
		FrameID:  "main",
		Type:     network.ResourceTypeXHR,
		Response: &network.Response{Status: 503},
	})
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want the document's 200", got)
	}
}

// TestTabTracksMainFrameChanges checks the tab follows its own frame
// across navigations rather than latching onto the id it started with.
func TestTabTracksMainFrameChanges(t *testing.T) {
	tab := &Tab{mainFrame: "main"}
	tab.observe(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "replacement"}})
	tab.observe(documentResponse("replacement", 200))
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want 200 from the new main frame", got)
	}
}

func TestTabIgnoresSubframeNavigations(t *testing.T) {
	tab := &Tab{mainFrame: "main"}
	tab.observe(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "embedded", ParentID: "main"}})
	tab.observe(documentResponse("embedded", 500))
	tab.observe(documentResponse("main", 200))
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want the main frame's 200", got)
	}
}

// TestTabClearsStatusBeforeNavigating checks a navigation that never
// gets a response cannot report the previous page's status.
func TestTabClearsStatusBeforeNavigating(t *testing.T) {
	tab := &Tab{mainFrame: "main"}
	tab.observe(documentResponse("main", 200))
	tab.clearStatus()
	if got := tab.HTTPStatus(); got != 0 {
		t.Fatalf("status %d after clearing, want 0", got)
	}
}

// TestReusesTabPerTarget checks a target is polled through the tab it
// already has, instead of opening a new one on every tick.
func TestReusesTabPerTarget(t *testing.T) {
	existing := &Tab{}
	browser := &Browser{tabs: map[string]*Tab{"owner/repo": existing}}
	tab, err := browser.Tab("owner/repo")
	if err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if tab != existing {
		t.Fatal("Tab opened a new tab for a target that already had one")
	}
}

// closedTab is a tab whose close is observable without a browser behind it.
func closedTab(closed *bool) *Tab {
	return &Tab{cancel: func() { *closed = true }}
}

// TestClosesIdleTabs checks a tab nothing has read for the idle timeout
// is closed instead of holding its page open for the rest of the run: the
// conversation of an issue whose agent has finished used to stay open until the
// run ended (issue #461).
func TestClosesIdleTabs(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	idled := false
	stale := closedTab(&idled)
	stale.resumeAt("https://github.com/owner/repo/issues/7")
	fresh := &Tab{}
	var logged []string
	browser := &Browser{
		tabs:   map[string]*Tab{"owner/repo": fresh, "comments": stale},
		used:   map[string]time.Time{"owner/repo": now.Add(-time.Minute), "comments": now.Add(-5 * time.Minute)},
		resume: map[string]string{},
		idle:   2 * time.Minute,
		now:    func() time.Time { return now },
		logf:   func(format string, v ...interface{}) { logged = append(logged, fmt.Sprintf(format, v...)) },
	}
	if _, err := browser.Tab("owner/repo"); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if !idled {
		t.Fatal("the idle tab was left open")
	}
	if _, ok := browser.tabs["comments"]; ok {
		t.Fatal("the idle tab is still held by the browser")
	}
	if _, ok := browser.tabs["owner/repo"]; !ok {
		t.Fatal("the tab being read was closed")
	}
	if got := browser.resume["comments"]; got != "https://github.com/owner/repo/issues/7" {
		t.Fatalf("remembered page %q, want the page the closed tab was showing", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "comments") || !strings.Contains(logged[0], "5m") {
		t.Fatalf("logged %q, want one line naming the tab and how long it was unread", logged)
	}
}

// TestKeepsLastTabOpen checks the reap never closes the browser's only
// tab: Chrome exits when its final page target closes, and the sign-in saved on
// shutdown is read off a tab of the still-running browser.
func TestKeepsLastTabOpen(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	closed := false
	only := closedTab(&closed)
	browser := &Browser{
		tabs: map[string]*Tab{"comments": only},
		used: map[string]time.Time{"comments": now.Add(-time.Hour)},
		idle: time.Minute,
		now:  func() time.Time { return now },
	}
	// Read a different tab so the idle one is not the tab being handed out;
	// opening one is not possible without a browser, so the reap is called as
	// the hand-out does.
	browser.mu.Lock()
	retired := browser.reapIdleTabs("owner/repo", now)
	browser.mu.Unlock()
	if len(retired) != 0 || closed {
		t.Fatal("the browser closed its only tab")
	}
}

// TestKeepsTabsWithoutIdleTimeout checks a browser that is not polling
// on a timer -- the one `glorp auth` signs in through -- never retires a tab.
func TestKeepsTabsWithoutIdleTimeout(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	closed := false
	browser := &Browser{
		tabs: map[string]*Tab{"auth": closedTab(&closed), "owner/repo": {}},
		used: map[string]time.Time{"auth": now.Add(-time.Hour), "owner/repo": now},
		now:  func() time.Time { return now },
	}
	if _, err := browser.Tab("owner/repo"); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if closed || len(browser.tabs) != 2 {
		t.Fatal("a browser with no idle timeout closed a tab")
	}
}

// TestTabReloadResumesRetiredPage checks a tab reopened in place of a
// retired one navigates to the page it is standing in for, because a reader
// polling an unchanged URL asks for a reload and reloading a tab that has never
// been anywhere loads nothing.
func TestTabReloadResumesRetiredPage(t *testing.T) {
	tab := &Tab{}
	if got := tab.resumeTarget(); got != "" {
		t.Fatalf("resume target %q on a tab that was never pointed anywhere, want none", got)
	}
	tab.resumeAt("https://github.com/owner/repo/issues/7")
	if got := tab.resumeTarget(); got != "https://github.com/owner/repo/issues/7" {
		t.Fatalf("resume target %q, want the retired tab's page", got)
	}
	tab.markNavigated()
	if got := tab.resumeTarget(); got != "" {
		t.Fatalf("resume target %q after the tab navigated itself, want none", got)
	}
}

func TestTabRejectedAfterClose(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	browser := &Browser{cancelAlloc: cancel, tabs: map[string]*Tab{}}
	if err := browser.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing twice must be safe: shutdown paths call it defensively.
	if err := browser.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := browser.Tab("owner/repo"); err == nil {
		t.Fatal("expected an error opening a tab on a closed browser")
	}
}

// TestAdoptsHeadedWindowOnce checks only the first tab of a headed
// browser attaches to the window Chrome opened by itself. Creating a target on
// top of that window is what left `glorp auth` showing an empty "New Tab"
// window beside the login window (issue #412), and a headless run has no window
// to adopt at all.
func TestAdoptsHeadedWindowOnce(t *testing.T) {
	for _, test := range []struct {
		name    string
		browser *Browser
		want    bool
	}{
		{
			name:    "headed first tab",
			browser: &Browser{config: Config{Headed: true}, cmd: &process{}, tabs: map[string]*Tab{}},
			want:    true,
		},
		{
			name:    "headed later tab",
			browser: &Browser{config: Config{Headed: true}, cmd: &process{}, tabs: map[string]*Tab{"owner/repo": {}}},
			want:    false,
		},
		{
			name:    "headless",
			browser: &Browser{config: Config{}, cmd: &process{}, tabs: map[string]*Tab{}},
			want:    false,
		},
		{
			name:    "no launched process",
			browser: &Browser{config: Config{Headed: true}, tabs: map[string]*Tab{}},
			want:    false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.browser.adoptsExistingWindow(); got != test.want {
				t.Fatalf("adoptsExistingWindow() = %t, want %t", got, test.want)
			}
		})
	}
}

// captureErrorf collects what errorf writes to the standard
// logger, restoring the logger's own destination and flags afterwards.
func captureErrorf(t *testing.T, s string, v ...any) string {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	out := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	}()
	errorf(s, v...)
	return buf.String()
}

func TestErrorfDropsUnhandledNodeEvents(t *testing.T) {
	// The exact message chromedp emits for a DOM event it does not model; it
	// says nothing about glorp's work, so it must not reach the terminal.
	got := captureErrorf(t, "unhandled node event %T", &dom.EventTopLayerElementsUpdated{})
	if got != "" {
		t.Fatalf("logged %q, want nothing", got)
	}
}

func TestErrorfKeepsOtherErrors(t *testing.T) {
	got := captureErrorf(t, "could not unmarshal event: %s", "boom")
	if want := "ERROR: could not unmarshal event: boom\n"; got != want {
		t.Fatalf("logged %q, want %q", got, want)
	}
}

// TestTabStopsWaitingForAStuckCommand checks a tab whose command has
// stopped returning fails the next read instead of joining it in blocking
// forever. The poll loop reads GitHub on the goroutine that dispatches work, so
// a command that never comes back used to stop a watch picking anything up for
// the rest of the run (issue #472).
func TestTabStopsWaitingForAStuckCommand(t *testing.T) {
	tab := &Tab{}
	stalled := make(chan struct{}, 1)
	tab.watchStalls(func() { stalled <- struct{}{} })
	// Take the tab as a command that never returns would.
	tab.gate() <- struct{}{}
	release, err := tab.acquire(10 * time.Millisecond)
	if err == nil {
		release()
		t.Fatal("a read of a stuck tab succeeded")
	}
	if !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("error %v, want one saying the tab is stuck", err)
	}
	select {
	case <-stalled:
	default:
		t.Fatal("the stuck tab was not reported as stalled")
	}
}

// TestTabAdmitsOneCommandAtATime checks the deadline did not cost the
// tab its serialization: a single CDP target cannot service two commands at
// once.
func TestTabAdmitsOneCommandAtATime(t *testing.T) {
	tab := &Tab{}
	release, err := tab.acquire(time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()
	if _, err := tab.acquire(10 * time.Millisecond); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

// TestDropsUnresponsiveTab checks a stalled tab is dropped so the next
// read of that target opens a fresh one, on the page the old one was showing,
// rather than queueing behind a command that is never coming back.
func TestDropsUnresponsiveTab(t *testing.T) {
	closed := false
	stuck := closedTab(&closed)
	stuck.resumeAt("https://github.com/owner/repo/issues/7")
	var logged []string
	browser := &Browser{
		tabs:   map[string]*Tab{"comments": stuck},
		used:   map[string]time.Time{"comments": time.Now()},
		resume: map[string]string{},
		logf:   func(format string, v ...interface{}) { logged = append(logged, fmt.Sprintf(format, v...)) },
	}
	browser.discardStalledTab("comments", stuck)
	if _, ok := browser.tabs["comments"]; ok {
		t.Fatal("the unresponsive tab is still held by the browser")
	}
	if got := browser.resume["comments"]; got != "https://github.com/owner/repo/issues/7" {
		t.Fatalf("remembered page %q, want the page the dropped tab was showing", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "comments") {
		t.Fatalf("logged %q, want one line naming the dropped tab", logged)
	}
	// Dropping the same tab twice must not drop its replacement.
	replacement := &Tab{}
	browser.tabs["comments"] = replacement
	browser.discardStalledTab("comments", stuck)
	if browser.tabs["comments"] != replacement {
		t.Fatal("a repeated drop took the replacement tab with it")
	}
}

// TestTabCloseDoesNotWaitForACommand checks shutdown is not held up by a
// command that has stopped returning: closing a tab cancels what it is doing
// rather than waiting it out.
func TestTabCloseDoesNotWaitForACommand(t *testing.T) {
	closed := false
	tab := closedTab(&closed)
	tab.gate() <- struct{}{}
	done := make(chan struct{})
	go func() { tab.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing a tab waited for the command in flight")
	}
	if !closed {
		t.Fatal("the tab was not cancelled")
	}
}
