package main

import (
	"bytes"
	"context"
	"log"
	"testing"

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
