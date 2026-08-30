package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// browserSignInBackoff is how long a run waits before offering to sign in again
// after an attempt that did not end signed in. A watch loop polls every few
// seconds, and a login window that was declined, timed out, or could never
// appear must not be re-opened on the next tick.
const browserSignInBackoff = 10 * time.Minute

// browserSignInBackoffMax caps the doubling. Past it the run keeps offering
// occasionally rather than giving up for good, because the thing that would fix
// it -- the user coming back to the machine -- can happen at any time.
const browserSignInBackoffMax = time.Hour

// browserSignInRecovery turns a signed-out read into a login window. The poll
// that read the signed-out page has already failed by the time this runs; the
// run stays alive either way, and the next tick reads the same page with a
// profile that is signed in.
//
// Chrome allows one process per --user-data-dir, so the flow has to hand the
// profile over rather than share it: stop the headless browser, open a headed
// one on the same profile at GitHub's login page, wait for the sign-in, close
// it, and start the headless browser again.
type browserSignInRecovery struct {
	config  browserConfig
	logf    func(string, ...interface{})
	timeout time.Duration

	// suspend and resume hand the profile to the login window and take it back.
	suspend func() error
	resume  func() error
	// login runs the headed flow. It is a field so tests can exercise the
	// recovery without a browser and without opening a window.
	login func(ctx context.Context, config browserConfig, out writerFunc, timeout time.Duration) (string, error)
	// headed reports whether a window could appear on this machine at all.
	headed func() error
	now    func() time.Time

	mu sync.Mutex
	// next is the earliest time another attempt may be made, and backoff is
	// how long the wait after the next failure will be.
	next    time.Time
	backoff time.Duration
}

// newBrowserSignInRecovery builds the recovery for a run's browser.
func newBrowserSignInRecovery(browser *Browser, config browserConfig, logf func(string, ...interface{})) *browserSignInRecovery {
	return &browserSignInRecovery{
		config:  config,
		logf:    logf,
		timeout: authLoginTimeout,
		suspend: browser.Suspend,
		resume:  browser.Resume,
		login: func(ctx context.Context, config browserConfig, out writerFunc, timeout time.Duration) (string, error) {
			return authLogin(ctx, config, out, timeout)
		},
		headed: func() error { return headedEnvironmentCheck() },
		now:    time.Now,
	}
}

// writerFunc adapts a log function to the io.Writer the auth flow writes its
// one progress line to, so that line lands in the run's log with everything
// else instead of on a stdout the terminal UI has taken over.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// logWriter returns a writer that forwards each written chunk to logf.
func (r *browserSignInRecovery) logWriter() writerFunc {
	return func(p []byte) (int, error) {
		if line := strings.TrimSpace(string(p)); line != "" {
			r.logf("browser: %s", line)
		}
		return len(p), nil
	}
}

// attempt runs the recovery for an error a read just failed with. Errors that
// are not a signed-out diagnosis are ignored, so callers can hand it every
// failure without classifying first.
func (r *browserSignInRecovery) attempt(ctx context.Context, err error) {
	if r == nil || err == nil || !errors.Is(err, errBrowserSignedOut) {
		return
	}
	if !r.claim() {
		// Already offered recently. The poll's own failure is logged by the
		// run loop, so staying silent here is what keeps a signed-out profile
		// from filling the log with the same offer every few seconds.
		return
	}
	r.logf("%s", signInAnnouncement(err))
	if reason := r.headed(); reason != nil {
		r.logf("browser: %v", reason)
		r.penalize()
		return
	}
	if err := r.suspend(); err != nil {
		r.logf("browser: could not stop the headless browser to open a sign-in window: %v", err)
		r.penalize()
		return
	}
	login, loginErr := r.login(ctx, r.config, r.logWriter(), r.timeout)
	// The headless browser is started again whichever way the sign-in went: a
	// run that gave up on the login still has to keep polling.
	if err := r.resume(); err != nil {
		r.logf("browser: could not restart the headless browser after the sign-in window closed: %v", err)
	}
	if loginErr != nil {
		r.logf("browser: sign-in did not complete: %v", loginErr)
		r.penalize()
		return
	}
	r.succeed()
	r.logf("browser: signed in to GitHub as %s; the profile is reused by later polls and later runs", login)
}

// signInAnnouncement is the line logged before a window is opened. It names the
// status when GitHub's own status is what gave the session away, so the log
// says what glorp saw rather than only what it decided.
func signInAnnouncement(err error) string {
	var signedOut *browserSignedOutError
	if errors.As(err, &signedOut) {
		if signedOut.Status != 0 {
			return fmt.Sprintf("browser: GitHub returned %d for %s — opening a browser window to sign in", signedOut.Status, signedOut.URL)
		}
		return fmt.Sprintf("browser: GitHub served a signed-out page for %s — opening a browser window to sign in", signedOut.URL)
	}
	return "browser: the browser profile is signed out of GitHub — opening a browser window to sign in"
}

// claim reports whether an attempt may run now, and records that one is being
// made. It is the whole of the "do not re-prompt every tick" rule.
func (r *browserSignInRecovery) claim() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.next.IsZero() && r.now().Before(r.next) {
		return false
	}
	// Held until the attempt finishes, so a second target failing the same
	// tick does not open a second window; a successful sign-in clears it.
	r.next = r.now().Add(browserSignInBackoffMax)
	return true
}

// penalize schedules the next offer after an attempt that did not sign in,
// doubling the wait each time up to the cap.
func (r *browserSignInRecovery) penalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backoff == 0 {
		r.backoff = browserSignInBackoff
	} else if r.backoff < browserSignInBackoffMax {
		r.backoff *= 2
		if r.backoff > browserSignInBackoffMax {
			r.backoff = browserSignInBackoffMax
		}
	}
	r.next = r.now().Add(r.backoff)
	r.logf("browser: not offering to sign in again for %s; run `glorp auth` to sign the profile in at any time", r.backoff)
}

// succeed clears the back-off after a sign-in that worked, so a session that
// expires later is recovered immediately rather than after the old wait.
func (r *browserSignInRecovery) succeed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next, r.backoff = time.Time{}, 0
}

// browserSignInGuard wraps browser mode's issue source so a read that came back
// signed out opens a login window before the next tick asks again. The error is
// still returned: the poll that hit a signed-out page genuinely failed, and the
// run loop's own retry is what makes the recovered profile take effect.
type browserSignInGuard struct {
	source   IssueSource
	recovery *browserSignInRecovery
}

func (g browserSignInGuard) ListIssues(ctx context.Context, target string) ([]Issue, error) {
	issues, err := g.source.ListIssues(ctx, target)
	if err != nil {
		g.recovery.attempt(ctx, err)
	}
	return issues, err
}
