package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Browser owns the single headless browser process a `glorp watch` run drives,
// and the tabs it reads pages in. Every target glorp watches gets one tab that
// is navigated or reloaded on each tick: opening a tab per poll would leak
// renderer processes for as long as the run lasts.
type Browser struct {
	cmd         *browserProcess
	allocCtx    context.Context
	cancelAlloc context.CancelFunc

	mu     sync.Mutex
	tabs   map[string]*BrowserTab
	closed bool
}

// startBrowser launches a headless browser against glorp's own profile and
// connects to it. The returned Browser must be closed, which is also what stops
// the browser process.
func startBrowser(ctx context.Context, config browserConfig) (*Browser, error) {
	process, err := launchBrowser(ctx, config)
	if err != nil {
		return nil, err
	}
	// The endpoint reported by the browser is already a complete WebSocket URL,
	// so chromedp is told not to rewrite it.
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, process.wsURL, chromedp.NoModifyURL)
	return &Browser{
		cmd:         process,
		allocCtx:    allocCtx,
		cancelAlloc: cancelAlloc,
		tabs:        map[string]*BrowserTab{},
	}, nil
}

// Profile reports the profile directory the browser was launched against, or
// an empty string when there is no launched process to ask (the tests' fakes).
func (b *Browser) Profile() string {
	if b == nil || b.cmd == nil {
		return ""
	}
	return b.cmd.profile
}

// Tab returns the tab glorp drives for a target, opening it on first use and
// reusing it afterwards. Names are the caller's own: one per watched target.
func (b *Browser) Tab(name string) (*BrowserTab, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, fmt.Errorf("browser is closed")
	}
	if tab, ok := b.tabs[name]; ok {
		return tab, nil
	}
	tab, err := newBrowserTab(b.allocCtx)
	if err != nil {
		return nil, fmt.Errorf("open browser tab for %s: %w", name, err)
	}
	b.tabs[name] = tab
	return tab, nil
}

// Close shuts the browser down: the tabs are closed, the connection is dropped,
// and the process is terminated so no browser survives the run that started it.
// It is safe to call more than once, so shutdown paths can call it defensively.
func (b *Browser) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	tabs := make([]*BrowserTab, 0, len(b.tabs))
	for _, tab := range b.tabs {
		tabs = append(tabs, tab)
	}
	b.tabs = nil
	b.mu.Unlock()
	for _, tab := range tabs {
		tab.close()
	}
	b.cancelAlloc()
	if b.cmd == nil {
		return nil
	}
	return stopChildProcess(b.cmd.cmd)
}

// BrowserTab is one reusable tab. Its methods serialize on the tab because a
// single CDP target cannot service two commands at once.
type BrowserTab struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc

	statusMu  sync.Mutex
	mainFrame cdp.FrameID
	status    int
}

// newBrowserTab opens a tab and starts watching it for the status of its
// main-frame navigations.
func newBrowserTab(allocCtx context.Context) (*BrowserTab, error) {
	ctx, cancel := chromedp.NewContext(allocCtx)
	tab := &BrowserTab{ctx: ctx, cancel: cancel}
	// Network events carry the status code, and the frame tree identifies which
	// of them belong to the tab itself rather than to an embedded frame.
	if err := chromedp.Run(ctx, network.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
		frame, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		tab.setMainFrame(frame.Frame.ID)
		return nil
	})); err != nil {
		cancel()
		return nil, err
	}
	chromedp.ListenTarget(ctx, tab.observe)
	return tab, nil
}

// observe records the outcome of the tab's own navigations. Documents loaded
// into embedded frames are ignored: the status glorp cares about is the one the
// page it asked for was served with.
func (t *BrowserTab) observe(event any) {
	switch event := event.(type) {
	case *page.EventFrameNavigated:
		// A frame with no parent is the tab's own; its id is stable across
		// navigations, but it is re-read here so a tab that outlives a
		// cross-process navigation keeps reporting statuses.
		if event.Frame.ParentID == "" {
			t.setMainFrame(event.Frame.ID)
		}
	case *network.EventResponseReceived:
		if event.Type == network.ResourceTypeDocument {
			t.recordStatus(event.FrameID, int(event.Response.Status))
		}
	}
}

func (t *BrowserTab) setMainFrame(id cdp.FrameID) {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	t.mainFrame = id
}

// recordStatus keeps the status of a document served to the tab's own frame.
func (t *BrowserTab) recordStatus(frame cdp.FrameID, status int) {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	if t.mainFrame != "" && frame != t.mainFrame {
		return
	}
	t.status = status
}

// clearStatus forgets the previous page's status, so a navigation that never
// produces a response cannot be mistaken for a successful one.
func (t *BrowserTab) clearStatus() {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	t.status = 0
}

// HTTPStatus reports the status code the tab's last main-frame navigation was
// served with, or zero when it has not navigated anywhere yet.
func (t *BrowserTab) HTTPStatus() int {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	return t.status
}

// Navigate points the tab at a URL.
func (t *BrowserTab) Navigate(url string) error {
	t.clearStatus()
	return t.run(chromedp.Navigate(url))
}

// Reload loads the tab's current URL again, which is how a watched page is
// polled without opening anything new.
func (t *BrowserTab) Reload() error {
	t.clearStatus()
	return t.run(chromedp.Reload())
}

// Eval evaluates a JavaScript expression in the tab and decodes its result into
// out, which may be nil when the result is not needed.
func (t *BrowserTab) Eval(js string, out any) error {
	if out == nil {
		var discard any
		out = &discard
	}
	return t.run(chromedp.Evaluate(js, out))
}

// Screenshot captures the full page as PNG bytes.
func (t *BrowserTab) Screenshot() ([]byte, error) {
	var image []byte
	if err := t.run(chromedp.FullScreenshot(&image, 90)); err != nil {
		return nil, err
	}
	return image, nil
}

// run executes actions against the tab, one caller at a time.
func (t *BrowserTab) run(actions ...chromedp.Action) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return chromedp.Run(t.ctx, actions...)
}

// close closes the tab.
func (t *BrowserTab) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancel()
}
