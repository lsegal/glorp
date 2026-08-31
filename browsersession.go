package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

// sessionCookieFileName is the file, inside the browser profile glorp owns,
// that holds the sign-in glorp carries from one browser process to the next.
// It lives in the profile directory because it belongs to that profile: point
// -browser-profile somewhere else and the sign-in there is the one restored.
const sessionCookieFileName = "glorp-session-cookies.json"

// sessionCookie is one cookie glorp carries across browser processes. It is a
// glorp-owned record rather than the CDP type so the file format does not move
// when the protocol's own cookie struct gains fields.
type sessionCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"httpOnly"`
	SameSite string `json:"sameSite,omitempty"`

	// session marks a cookie the browser holds only in memory. It is not
	// serialized: everything in the file is a session cookie by construction,
	// and the flag exists to pick those out of the browser's full jar.
	session bool
}

// cookieJar is the browser's cookie store, narrowed to the two operations the
// sign-in handover needs. It is an interface so the save and restore logic can
// be exercised without a browser.
type cookieJar interface {
	readCookies() ([]sessionCookie, error)
	writeCookies(cookies []sessionCookie) error
}

// sessionCookiePath reports where a profile's carried-over sign-in is kept.
func sessionCookiePath(profile string) string {
	return filepath.Join(profile, sessionCookieFileName)
}

// sessionOnly keeps the cookies the browser would otherwise lose. Cookies with
// an expiry are already written to the profile's own cookie database by the
// browser, so re-injecting them would only duplicate work the browser did.
func sessionOnly(cookies []sessionCookie) []sessionCookie {
	kept := make([]sessionCookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie.session {
			kept = append(kept, cookie)
		}
	}
	return kept
}

// saveSessionCookies writes a profile's session cookies to the profile so the
// next browser process on it starts signed in.
//
// GitHub's sign-in lives in session cookies (`user_session` and friends), which
// a browser keeps in memory and drops when its process exits, while only the
// crumbs around them (`logged_in`, `_octo`, `_device_id`) carry an expiry and
// reach the profile's cookie database. Persisting the profile directory alone
// therefore persists everything about the sign-in except the sign-in, which is
// why `glorp auth` had to be run again for every watch (issue #414).
func saveSessionCookies(profile string, jar cookieJar) error {
	if profile == "" {
		return nil
	}
	cookies, err := jar.readCookies()
	if err != nil {
		return fmt.Errorf("read browser session cookies: %w", err)
	}
	path := sessionCookiePath(profile)
	cookies = sessionOnly(cookies)
	if len(cookies) == 0 {
		// A profile that was signed out has no sign-in left to carry, and a
		// stale file would keep offering the browser a session GitHub has
		// already invalidated.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("clear browser session cookies %s: %w", path, err)
		}
		return nil
	}
	data, err := json.Marshal(cookies)
	if err != nil {
		return fmt.Errorf("encode browser session cookies: %w", err)
	}
	// The file holds a live GitHub session, so it is written no wider than the
	// profile directory it sits in.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write browser session cookies %s: %w", path, err)
	}
	return nil
}

// restoreSessionCookies puts a previously saved sign-in back into a freshly
// launched browser. A profile that has never been signed in has no file and is
// not an error: the run is simply signed out, which the existing sign-in
// recovery already handles.
func restoreSessionCookies(profile string, jar cookieJar) error {
	if profile == "" {
		return nil
	}
	path := sessionCookiePath(profile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read browser session cookies %s: %w", path, err)
	}
	var cookies []sessionCookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		// A file glorp cannot read is a file glorp wrote wrong or something
		// else corrupted; dropping it costs one sign-in, keeping it would fail
		// every launch from here on.
		_ = os.Remove(path)
		return fmt.Errorf("decode browser session cookies %s: %w", path, err)
	}
	if len(cookies) == 0 {
		return nil
	}
	if err := jar.writeCookies(cookies); err != nil {
		return fmt.Errorf("restore browser session cookies: %w", err)
	}
	return nil
}

// browserTabJar is a browser's cookie store reached through one of its tabs.
// Storage commands are browser-scoped, but CDP still needs a target to carry
// them, so the jar borrows a tab the run already has rather than opening one:
// a headed browser's first tab is the window `glorp auth` signs in through
// (issue #412), and taking it for cookie work would put a second window on
// screen again.
type browserTabJar struct{ tab *BrowserTab }

// readCookies reports every cookie the browser currently holds.
func (j browserTabJar) readCookies() ([]sessionCookie, error) {
	var jar []*network.Cookie
	if err := j.tab.run(chromedp.ActionFunc(func(ctx context.Context) error {
		cookies, err := storage.GetCookies().Do(ctx)
		jar = cookies
		return err
	})); err != nil {
		return nil, err
	}
	cookies := make([]sessionCookie, 0, len(jar))
	for _, cookie := range jar {
		if cookie == nil {
			continue
		}
		cookies = append(cookies, sessionCookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Secure:   cookie.Secure,
			HTTPOnly: cookie.HTTPOnly,
			SameSite: cookie.SameSite.String(),
			session:  cookie.Session,
		})
	}
	return cookies, nil
}

// writeCookies injects cookies into the browser. It is called before anything
// navigates, so the first page load of a run is already made as the signed-in
// user rather than being redirected to a login wall first.
func (j browserTabJar) writeCookies(cookies []sessionCookie) error {
	params := make([]*network.CookieParam, 0, len(cookies))
	for _, cookie := range cookies {
		params = append(params, &network.CookieParam{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Secure:   cookie.Secure,
			HTTPOnly: cookie.HTTPOnly,
			SameSite: network.CookieSameSite(cookie.SameSite),
		})
	}
	return j.tab.run(chromedp.ActionFunc(func(ctx context.Context) error {
		return storage.SetCookies(params).Do(ctx)
	}))
}

// saveSession and restoreSession carry the profile's sign-in across the browser
// processes a run starts and stops, through a tab the run already owns. Both
// are best-effort: a browser that cannot be asked for its cookies still watches
// GitHub, and a sign-in that could not be carried is recovered by the sign-in
// guard exactly as it is today. The error is returned so callers may report it.
func (b *Browser) saveSession(tab *BrowserTab) error {
	return saveSessionCookies(b.Profile(), browserTabJar{tab})
}

func (b *Browser) restoreSession(tab *BrowserTab) error {
	return restoreSessionCookies(b.Profile(), browserTabJar{tab})
}
