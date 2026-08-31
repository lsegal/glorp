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
	// ctx and config are what the browser was started from, kept so the same
	// browser can be stopped and started again on the same profile without the
	// readers that hold it having to be rebuilt (issue #379).
	ctx    context.Context
	config browserConfig

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
	browser := &Browser{
		ctx:         ctx,
		config:      config,
		cmd:         process,
		allocCtx:    allocCtx,
		cancelAlloc: cancelAlloc,
		tabs:        map[string]*BrowserTab{},
	}
	// A browser process starts with none of the session cookies the last one
	// held, so the sign-in is put back before anything navigates (issue #414).
	_ = browser.restoreSession()
	return browser, nil
}

// Suspend stops the browser process and drops its tabs, leaving the Browser
// itself usable. Chrome allows exactly one process per --user-data-dir, so
// putting a login window on the same profile the watch loop reads GitHub with
// means getting out of the way first; Resume starts a new process afterwards
// and the readers holding this Browser keep working, because they ask for their
// tab by name on every read rather than holding one.
func (b *Browser) Suspend() error { return b.Close() }

// Resume starts the browser again after Suspend. Tabs are not restored: a tab
// belongs to the process that owned it, and every reader re-opens the one it
// needs on its next read, navigating it where it needs to be anyway.
func (b *Browser) Resume() error {
	relaunched, err := b.relaunch()
	if err != nil || !relaunched {
		return err
	}
	// The process the sign-in window signed in is not the process the watch
	// loop reads GitHub with, so the sign-in is carried over here too
	// (issue #414). It runs outside the lock because it reads the tabs.
	_ = b.restoreSession()
	return nil
}

// relaunch starts a new browser process for a suspended Browser and reports
// whether it started one, so Resume can do its cookie work unlocked.
func (b *Browser) relaunch() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		return false, nil
	}
	process, err := launchBrowser(b.ctx, b.config)
	if err != nil {
		return false, err
	}
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(b.ctx, process.wsURL, chromedp.NoModifyURL)
	b.cmd, b.allocCtx, b.cancelAlloc = process, allocCtx, cancelAlloc
	b.tabs = map[string]*BrowserTab{}
	b.closed = false
	return true, nil
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
	// Saved before the process is stopped, because the sign-in only exists in
	// the memory of the process about to be terminated (issue #414).
	_ = b.saveSession()
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
