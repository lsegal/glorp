package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	cdptarget "github.com/chromedp/cdproto/target"
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
	// used is when each open tab was last handed out, idle is how long a tab
	// may go unread before it is closed, and resume is the page a closed tab
	// was showing so its replacement can pick the same one up again. Together
	// they retire a tab glorp has stopped reading -- the conversation of an
	// issue whose agent has finished, most visibly -- rather than leaving its
	// page open for the rest of the run (issue #461). A zero idle never
	// retires anything, which is what a browser that is not polling on a timer
	// (the one `glorp auth` signs in through) wants.
	used   map[string]time.Time
	idle   time.Duration
	resume map[string]string
	now    func() time.Time
	logf   func(string, ...interface{})
	// queue paces the page loads every tab of this browser makes, so a tick
	// that reloads many targets trickles its requests out instead of firing
	// them all at once (issue #450). It is nil for a browser that is not
	// polling on a timer, such as the one `glorp auth` signs in through.
	queue *browserLoadQueue
	// restored records that this browser process has already been given the
	// sign-in saved by the last one, so it happens once per process.
	restored bool
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
		used:        map[string]time.Time{},
		resume:      map[string]string{},
	}
	return browser, nil
}

// SetLoadQueue attaches the run's shared page-load queue, which every tab this
// browser opens paces its navigations and reloads through. It is set before
// anything is read, because tabs are opened lazily on their first read.
func (b *Browser) SetLoadQueue(queue *browserLoadQueue) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue = queue
	for _, tab := range b.tabs {
		tab.paceWith(b.ctx, queue)
	}
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
	_, err := b.relaunch()
	return err
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
	b.used, b.resume = map[string]time.Time{}, map[string]string{}
	b.closed = false
	// The new process holds none of the old one's session cookies, so the
	// sign-in is carried into it again on its first tab (issue #414).
	b.restored = false
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
	tab, retired, err := b.tabFor(name)
	// Retired tabs are closed outside the browser lock, because closing one
	// waits for whatever it was last doing to finish and no other reader
	// should be held up by that.
	for _, closing := range retired {
		closing.tab.close()
		b.logTabRetired(closing.name, closing.idle)
	}
	if err != nil {
		return nil, err
	}
	return tab, nil
}

// retiredTab is a tab the reap closed, kept only long enough to close and
// report it once the browser lock has been released.
type retiredTab struct {
	tab  *BrowserTab
	name string
	idle time.Duration
}

// tabFor does Tab's bookkeeping under the browser lock: it hands out the named
// tab, opening one when there is none, and names the tabs that have gone idle
// long enough to be closed.
func (b *Browser) tabFor(name string) (*BrowserTab, []retiredTab, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, fmt.Errorf("browser is closed")
	}
	now := b.clock()
	if tab, ok := b.tabs[name]; ok {
		b.used[name] = now
		return tab, b.reapIdleTabs(name, now), nil
	}
	tab, err := b.openTab()
	if err != nil {
		return nil, nil, fmt.Errorf("open browser tab for %s: %w", name, err)
	}
	// Every tab loads pages through the run's one queue, so the stagger is a
	// property of the run rather than of any single target's tab.
	tab.paceWith(b.ctx, b.queue)
	// A tab reopened under a name that was retired starts out pointed at the
	// page the old one was showing, so a reader that polls by reloading an
	// unchanged URL loads that page again instead of reloading a blank tab.
	if url := b.resume[name]; url != "" {
		delete(b.resume, name)
		tab.resumeAt(url)
	}
	b.tabs[name] = tab
	b.used[name] = now
	// The sign-in saved by the last browser process is put back on the first
	// tab this one opens, before it navigates anywhere, so the run's first page
	// load is already made as the signed-in user (issue #414). It rides the tab
	// the run wanted anyway rather than opening one of its own, which would
	// cost a headed `glorp auth` the window adoption above.
	if !b.restored {
		b.restored = true
		_ = b.restoreSession(tab)
	}
	return tab, b.reapIdleTabs(name, now), nil
}

// SetTabIdleTimeout sets how long a tab may go unread before the browser closes
// it, and where the closures are logged. A run watching a handful of targets
// reloads their pages every tick, so only a page glorp has genuinely stopped
// reading ages out.
func (b *Browser) SetTabIdleTimeout(idle time.Duration, logf func(string, ...interface{})) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.idle, b.logf = idle, logf
}

// reapIdleTabs drops every tab that has gone unread for the idle timeout,
// returning them for the caller to close once the lock is released. keep is the
// tab being handed out, which is never retired however long it sat idle before
// this read.
//
// The last remaining tab is kept whatever its age: Chrome exits when its final
// page target closes, and the sign-in a run saves on shutdown is read off a tab
// of the still-running browser.
func (b *Browser) reapIdleTabs(keep string, now time.Time) []retiredTab {
	if b.idle <= 0 {
		return nil
	}
	var retired []retiredTab
	for name, tab := range b.tabs {
		if len(b.tabs) <= 1 {
			break
		}
		if name == keep {
			continue
		}
		idle := now.Sub(b.used[name])
		if idle < b.idle {
			continue
		}
		delete(b.tabs, name)
		delete(b.used, name)
		// Remembered so a later read of the same target reopens on the page it
		// was left on rather than on a blank tab.
		if url := tab.currentURL(); url != "" {
			b.resume[name] = url
		}
		retired = append(retired, retiredTab{tab: tab, name: name, idle: idle})
	}
	return retired
}

// logTabRetired reports a closed tab, so a run that quietly drops a page it had
// open says so.
func (b *Browser) logTabRetired(name string, idle time.Duration) {
	b.mu.Lock()
	logf := b.logf
	b.mu.Unlock()
	if logf == nil {
		return
	}
	logf("closing browser tab for %s: unread for %s", name, formatInterval(idle))
}

// clock reports the browser's current time, which tests replace.
func (b *Browser) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// anyTab returns one of the browser's open tabs, or nil when it has none.
func (b *Browser) anyTab() *BrowserTab {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tab := range b.tabs {
		return tab
	}
	return nil
}

// adoptsExistingWindow reports whether the next tab should attach to a window
// the browser already has open instead of creating one. Only the first tab of a
// headed browser qualifies: Chrome opens a window of its own as soon as it
// starts, so creating a target on top of it is what put a second, empty window
// on screen during `glorp auth` (issue #412). Headless runs have no window to
// adopt, and every later tab is a genuinely new one.
func (b *Browser) adoptsExistingWindow() bool {
	return b.config.Headed && len(b.tabs) == 0 && b.cmd != nil
}

// openTab opens the tab a target is read through, adopting the browser's own
// window when there is one to adopt. Adoption is best effort: a browser that
// reports no page, or a window that cannot be attached to, falls back to a
// freshly created tab, because showing an extra window is a far better outcome
// than failing to sign in at all.
func (b *Browser) openTab() (*BrowserTab, error) {
	if b.adoptsExistingWindow() {
		if id, err := browserOpenPageTarget(b.ctx, browserDebugURL(b.cmd.port), browserTargetTimeout); err == nil {
			if tab, err := newBrowserTab(b.allocCtx, chromedp.WithTargetID(cdptarget.ID(id))); err == nil {
				return tab, nil
			}
		}
	}
	return newBrowserTab(b.allocCtx)
}

// Close shuts the browser down: the tabs are closed, the connection is dropped,
// and the process is terminated so no browser survives the run that started it.
// It is safe to call more than once, so shutdown paths can call it defensively.
func (b *Browser) Close() error {
	// Saved before the process is stopped, because the sign-in only exists in
	// the memory of the process about to be terminated (issue #414). A browser
	// that never opened a tab has nothing to save and no target to ask.
	if tab := b.anyTab(); tab != nil {
		_ = b.saveSession(tab)
	}
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
	b.used, b.resume = nil, nil
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

	// queue paces this tab's page loads, and paceCtx is the run's context, so
	// a tab waiting for its slot is released when the run shuts down. A nil
	// queue loads pages the moment it is asked to. loadURL is the URL the tab
	// was last pointed at, which is what a reload is loading and so how the
	// queue counts it.
	queue   *browserLoadQueue
	paceCtx context.Context
	loadURL string
	// navigated records that this tab has actually loaded loadURL, which a tab
	// reopened in place of a retired one has not: its reload navigates instead.
	navigated bool
}

// resumeAt tells a tab which page it is meant to be showing without loading it,
// so a reader that polls by reloading an unchanged URL navigates there on its
// next read instead of reloading a tab that has never been anywhere.
func (t *BrowserTab) resumeAt(url string) {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	t.loadURL = url
	t.navigated = false
}

// currentURL is the page the tab was last pointed at, or "" when it has not
// been pointed anywhere.
func (t *BrowserTab) currentURL() string {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	return t.loadURL
}

// paceWith points the tab at the queue its page loads are staggered by.
func (t *BrowserTab) paceWith(ctx context.Context, queue *browserLoadQueue) {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	t.queue, t.paceCtx = queue, ctx
}

// awaitLoadSlot holds a load of url until the run's queue says its turn has
// come, and remembers the URL so a later reload queues as the same page. A run
// being shut down mid-wait stops waiting: the load still goes ahead, and the
// cancelled browser context fails it the same way it fails any other command
// issued during shutdown.
//
// An empty url is a reload of a tab that has never navigated, which is not a
// page glorp asked for; it is paced under one shared key rather than being
// counted as demand of its own.
func (t *BrowserTab) awaitLoadSlot(url string) {
	t.statusMu.Lock()
	if url == "" {
		url = t.loadURL
	} else {
		t.loadURL = url
	}
	queue, ctx := t.queue, t.paceCtx
	t.statusMu.Unlock()
	if queue == nil {
		return
	}
	queue.wait(ctx, url)
}

// newBrowserTab opens a tab and starts watching it for the status of its
// main-frame navigations.
func newBrowserTab(allocCtx context.Context, opts ...chromedp.ContextOption) (*BrowserTab, error) {
	ctx, cancel := chromedp.NewContext(allocCtx, append([]chromedp.ContextOption{chromedp.WithErrorf(browserErrorf)}, opts...)...)
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
	t.awaitLoadSlot(url)
	t.clearStatus()
	t.markNavigated()
	return t.run(chromedp.Navigate(url))
}

// markNavigated records that the tab has been sent to its current URL.
func (t *BrowserTab) markNavigated() {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	t.navigated = true
}

// Reload loads the tab's current URL again, which is how a watched page is
// polled without opening anything new.
func (t *BrowserTab) Reload() error {
	// A tab that has never navigated but knows where it belongs is one that
	// replaced a retired tab: reloading it would load nothing, so the page it
	// is standing in for is opened instead (issue #461).
	t.statusMu.Lock()
	resumeURL := ""
	if !t.navigated && t.loadURL != "" {
		resumeURL = t.loadURL
	}
	t.statusMu.Unlock()
	if resumeURL != "" {
		return t.Navigate(resumeURL)
	}
	t.awaitLoadSlot("")
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

// chromedp logs an error for every DOM event it has no case for in its own
// node-tree bookkeeping, and browsers keep adding events it does not model:
// `dom.topLayerElementsUpdated`, for instance, fires whenever a dialog or a
// popover enters the top layer. Those events say nothing about glorp's own
// work, so they are dropped instead of being printed to the terminal a user is
// watching (issue #438). Anything else chromedp reports is still surfaced.
func browserErrorf(s string, v ...any) {
	if strings.HasPrefix(s, "unhandled node event") {
		return
	}
	log.Printf("ERROR: "+s, v...)
}
