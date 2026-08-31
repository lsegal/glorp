package main

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

func TestBrowserTabRecordsMainFrameStatus(t *testing.T) {
	tab := &BrowserTab{mainFrame: "main"}
	if got := tab.HTTPStatus(); got != 0 {
		t.Fatalf("status %d before any navigation, want 0", got)
	}
	tab.observe(documentResponse("main", 404))
	if got := tab.HTTPStatus(); got != 404 {
		t.Fatalf("status %d, want 404", got)
	}
}

// TestBrowserTabIgnoresSubframeStatus guards the status glorp reads against
// being replaced by whatever an embedded frame on the page was served with.
func TestBrowserTabIgnoresSubframeStatus(t *testing.T) {
	tab := &BrowserTab{mainFrame: "main"}
	tab.observe(documentResponse("main", 200))
	tab.observe(documentResponse("embedded", 500))
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want the main frame's 200", got)
	}
}

func TestBrowserTabIgnoresNonDocumentResponses(t *testing.T) {
	tab := &BrowserTab{mainFrame: "main"}
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

// TestBrowserTabTracksMainFrameChanges checks the tab follows its own frame
// across navigations rather than latching onto the id it started with.
func TestBrowserTabTracksMainFrameChanges(t *testing.T) {
	tab := &BrowserTab{mainFrame: "main"}
	tab.observe(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "replacement"}})
	tab.observe(documentResponse("replacement", 200))
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want 200 from the new main frame", got)
	}
}

func TestBrowserTabIgnoresSubframeNavigations(t *testing.T) {
	tab := &BrowserTab{mainFrame: "main"}
	tab.observe(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "embedded", ParentID: "main"}})
	tab.observe(documentResponse("embedded", 500))
	tab.observe(documentResponse("main", 200))
	if got := tab.HTTPStatus(); got != 200 {
		t.Fatalf("status %d, want the main frame's 200", got)
	}
}

// TestBrowserTabClearsStatusBeforeNavigating checks a navigation that never
// gets a response cannot report the previous page's status.
func TestBrowserTabClearsStatusBeforeNavigating(t *testing.T) {
	tab := &BrowserTab{mainFrame: "main"}
	tab.observe(documentResponse("main", 200))
	tab.clearStatus()
	if got := tab.HTTPStatus(); got != 0 {
		t.Fatalf("status %d after clearing, want 0", got)
	}
}

// TestBrowserReusesTabPerTarget checks a target is polled through the tab it
// already has, instead of opening a new one on every tick.
func TestBrowserReusesTabPerTarget(t *testing.T) {
	existing := &BrowserTab{}
	browser := &Browser{tabs: map[string]*BrowserTab{"owner/repo": existing}}
	tab, err := browser.Tab("owner/repo")
	if err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if tab != existing {
		t.Fatal("Tab opened a new tab for a target that already had one")
	}
}

// closedTab is a tab whose close is observable without a browser behind it.
func closedTab(closed *bool) *BrowserTab {
	return &BrowserTab{cancel: func() { *closed = true }}
}

// TestBrowserClosesIdleTabs checks a tab nothing has read for the idle timeout
// is closed instead of holding its page open for the rest of the run: the
// conversation of an issue whose agent has finished used to stay open until the
// run ended (issue #461).
func TestBrowserClosesIdleTabs(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	idled := false
	stale := closedTab(&idled)
	stale.resumeAt("https://github.com/owner/repo/issues/7")
	fresh := &BrowserTab{}
	var logged []string
	browser := &Browser{
		tabs:   map[string]*BrowserTab{"owner/repo": fresh, "comments": stale},
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

// TestBrowserKeepsLastTabOpen checks the reap never closes the browser's only
// tab: Chrome exits when its final page target closes, and the sign-in saved on
// shutdown is read off a tab of the still-running browser.
func TestBrowserKeepsLastTabOpen(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	closed := false
	only := closedTab(&closed)
	browser := &Browser{
		tabs: map[string]*BrowserTab{"comments": only},
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

// TestBrowserKeepsTabsWithoutIdleTimeout checks a browser that is not polling
// on a timer -- the one `glorp auth` signs in through -- never retires a tab.
func TestBrowserKeepsTabsWithoutIdleTimeout(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	closed := false
	browser := &Browser{
		tabs: map[string]*BrowserTab{"auth": closedTab(&closed), "owner/repo": {}},
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

// TestBrowserTabReloadResumesRetiredPage checks a tab reopened in place of a
// retired one navigates to the page it is standing in for, because a reader
// polling an unchanged URL asks for a reload and reloading a tab that has never
// been anywhere loads nothing.
func TestBrowserTabReloadResumesRetiredPage(t *testing.T) {
	tab := &BrowserTab{}
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

func TestBrowserTabRejectedAfterClose(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	browser := &Browser{cancelAlloc: cancel, tabs: map[string]*BrowserTab{}}
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

// TestBrowserAdoptsHeadedWindowOnce checks only the first tab of a headed
// browser attaches to the window Chrome opened by itself. Creating a target on
// top of that window is what left `glorp auth` showing an empty "New Tab"
// window beside the login window (issue #412), and a headless run has no window
// to adopt at all.
func TestBrowserAdoptsHeadedWindowOnce(t *testing.T) {
	for _, test := range []struct {
		name    string
		browser *Browser
		want    bool
	}{
		{
			name:    "headed first tab",
			browser: &Browser{config: browserConfig{Headed: true}, cmd: &browserProcess{}, tabs: map[string]*BrowserTab{}},
			want:    true,
		},
		{
			name:    "headed later tab",
			browser: &Browser{config: browserConfig{Headed: true}, cmd: &browserProcess{}, tabs: map[string]*BrowserTab{"owner/repo": {}}},
			want:    false,
		},
		{
			name:    "headless",
			browser: &Browser{config: browserConfig{}, cmd: &browserProcess{}, tabs: map[string]*BrowserTab{}},
			want:    false,
		},
		{
			name:    "no launched process",
			browser: &Browser{config: browserConfig{Headed: true}, tabs: map[string]*BrowserTab{}},
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

// captureBrowserErrorf collects what browserErrorf writes to the standard
// logger, restoring the logger's own destination and flags afterwards.
func captureBrowserErrorf(t *testing.T, s string, v ...any) string {
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
	browserErrorf(s, v...)
	return buf.String()
}

func TestBrowserErrorfDropsUnhandledNodeEvents(t *testing.T) {
	// The exact message chromedp emits for a DOM event it does not model; it
	// says nothing about glorp's work, so it must not reach the terminal.
	got := captureBrowserErrorf(t, "unhandled node event %T", &dom.EventTopLayerElementsUpdated{})
	if got != "" {
		t.Fatalf("logged %q, want nothing", got)
	}
}

func TestBrowserErrorfKeepsOtherErrors(t *testing.T) {
	got := captureBrowserErrorf(t, "could not unmarshal event: %s", "boom")
	if want := "ERROR: could not unmarshal event: boom\n"; got != want {
		t.Fatalf("logged %q, want %q", got, want)
	}
}
