package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

// The tests here drive a real Chromium-based browser, which is what the fake
// cookie jar in browsersession_test.go cannot do: they are the only thing that
// proves the browser accepts the cookies glorp writes back to it (issue #420).
// The risk they cover is entirely in the browser's own rules — the `__Host-`
// prefix, `SameSite` round-tripping, `Secure` and `HttpOnly` — so a fake jar
// that stores whatever it is handed would pass no matter what glorp wrote.
//
// A machine with no browser installed skips them rather than failing: the rest
// of the suite has no browser dependency and neither should a `go test ./...`
// on a developer machine without Chrome.

// liveBrowserProfile starts a browser on a fresh profile directory, returning
// both so the caller can start a second browser on the same profile and see
// what the first one left behind.
func liveBrowserProfile(t *testing.T, profile string) *Browser {
	t.Helper()
	if _, err := findBrowserBinary(""); err != nil {
		t.Skipf("no Chromium-based browser installed: %v", err)
	}
	browser, err := startBrowser(context.Background(), browserConfig{Profile: profile})
	if err != nil {
		t.Skipf("browser would not start: %v", err)
	}
	return browser
}

// githubShapedCookieServer serves the cookies GitHub sets on a sign-in, with
// the attributes GitHub sets them with: `user_session` and `_gh_sess` are
// plain session cookies, `__Host-user_session_same_site` carries the prefix
// whose rules a browser enforces on the way back in, and `logged_in` is one of
// the expiring crumbs glorp deliberately leaves to the browser's own store.
// Its /echo path reports the cookies the browser actually sent, which is the
// only evidence that a restored cookie is live rather than merely present.
func githubShapedCookieServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			fmt.Fprint(w, r.Header.Get("Cookie"))
			return
		}
		w.Header().Add("Set-Cookie", "user_session=session-value; path=/; secure; HttpOnly; SameSite=Lax")
		w.Header().Add("Set-Cookie", "__Host-user_session_same_site=same-site-value; path=/; secure; HttpOnly; SameSite=Strict")
		w.Header().Add("Set-Cookie", "_gh_sess=gh-sess-value; path=/; secure; HttpOnly")
		w.Header().Add("Set-Cookie", "logged_in=yes; path=/; secure; HttpOnly; SameSite=Lax; Max-Age=31536000")
		fmt.Fprint(w, "signed in")
	}))
}

func TestBrowserCarriesSessionCookiesToTheNextProcess(t *testing.T) {
	server := githubShapedCookieServer()
	defer server.Close()
	profile := t.TempDir()

	signIn := liveBrowserProfile(t, profile)
	tab, err := signIn.Tab("sign-in")
	if err != nil {
		t.Fatalf("open tab: %v", err)
	}
	if err := tab.Navigate(server.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	// Close saves the session on its way out, exactly as a watch run's browser
	// restart and `glorp auth` both do.
	if err := signIn.Close(); err != nil {
		t.Fatalf("close signed-in browser: %v", err)
	}

	data, err := os.ReadFile(sessionCookiePath(profile))
	if err != nil {
		t.Fatalf("read saved session: %v", err)
	}
	var saved []sessionCookie
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode saved session: %v", err)
	}
	byName := map[string]sessionCookie{}
	for _, cookie := range saved {
		byName[cookie.Name] = cookie
	}
	for _, name := range []string{"user_session", "__Host-user_session_same_site", "_gh_sess"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("saved session is missing %s, got %v", name, saved)
		}
	}
	if _, ok := byName["logged_in"]; ok {
		t.Errorf("expiring cookie logged_in was saved; the browser's own store owns it")
	}
	if got := byName["user_session"]; got.Value != "session-value" || !got.Secure || !got.HTTPOnly || got.SameSite != "Lax" || got.Path != "/" {
		t.Errorf("user_session round-tripped as %+v", got)
	}
	if got := byName["__Host-user_session_same_site"]; got.SameSite != "Strict" || !got.Secure {
		t.Errorf("__Host-user_session_same_site round-tripped as %+v", got)
	}

	watch := liveBrowserProfile(t, profile)
	defer watch.Close()
	// The restore rides the first tab the run opens, before it navigates.
	restored, err := watch.Tab("watch")
	if err != nil {
		t.Fatalf("open tab on restored profile: %v", err)
	}
	jar, err := browserTabJar{restored}.readCookies()
	if err != nil {
		t.Fatalf("read restored jar: %v", err)
	}
	accepted := map[string]sessionCookie{}
	for _, cookie := range jar {
		accepted[cookie.Name] = cookie
	}
	for name, want := range byName {
		got, ok := accepted[name]
		if !ok {
			t.Errorf("browser rejected restored cookie %s (%+v)", name, want)
			continue
		}
		if got.Value != want.Value || got.Domain != want.Domain || got.Path != want.Path ||
			got.Secure != want.Secure || got.HTTPOnly != want.HTTPOnly || got.SameSite != want.SameSite {
			t.Errorf("restored cookie %s changed: saved %+v, browser holds %+v", name, want, got)
		}
		if !got.session {
			t.Errorf("restored cookie %s is no longer a session cookie: %+v", name, got)
		}
	}

	// Present in the jar is not the same as sent on the wire: ask the server
	// what it actually received on this process's first request.
	if err := restored.Navigate(server.URL + "/echo"); err != nil {
		t.Fatalf("navigate restored tab: %v", err)
	}
	var sent string
	if err := restored.Eval("document.body.innerText", &sent); err != nil {
		t.Fatalf("read echoed cookies: %v", err)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !cookieSent(sent, name, byName[name].Value) {
			t.Errorf("restored %s was not sent to the server; request carried %q", name, sent)
		}
	}
}

// cookieSent reports whether an echoed Cookie header carries a name=value pair.
func cookieSent(header, name, value string) bool {
	for _, pair := range strings.Split(header, "; ") {
		if pair == name+"="+value {
			return true
		}
	}
	return false
}

func TestBrowserRestoresCarriedSessionBeforeNavigating(t *testing.T) {
	server := githubShapedCookieServer()
	defer server.Close()
	profile := t.TempDir()
	path := sessionCookiePath(profile)
	// A sign-in saved by an earlier run, written by hand so the injection is
	// tested against the file format rather than against whatever the browser
	// happened to hand back a moment earlier.
	saved := `[{"name":"user_session","value":"carried","domain":"127.0.0.1","path":"/","secure":true,"httpOnly":true,"sameSite":"Lax"}]`
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatalf("seed saved session: %v", err)
	}

	browser := liveBrowserProfile(t, profile)
	defer browser.Close()
	tab, err := browser.Tab("watch")
	if err != nil {
		t.Fatalf("open tab: %v", err)
	}
	// The first navigation of the run must already carry the sign-in: that is
	// the whole point of restoring on the tab rather than after the first read.
	if err := tab.Navigate(server.URL + "/echo"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	var sent string
	if err := tab.Eval("document.body.innerText", &sent); err != nil {
		t.Fatalf("read echoed cookies: %v", err)
	}
	if !cookieSent(sent, "user_session", "carried") {
		t.Fatalf("first request of the run did not carry the saved sign-in; it sent %q", sent)
	}
}

func TestBrowserSavesNothingForASignedOutProfile(t *testing.T) {
	profile := t.TempDir()
	browser := liveBrowserProfile(t, profile)
	if _, err := browser.Tab("watch"); err != nil {
		t.Fatalf("open tab: %v", err)
	}
	// Nothing signed this profile in, so there is no sign-in to carry and no
	// file should appear for the next run to replay.
	if err := browser.Close(); err != nil {
		t.Fatalf("close browser: %v", err)
	}
	if _, err := os.Stat(sessionCookiePath(profile)); !os.IsNotExist(err) {
		data, _ := os.ReadFile(sessionCookiePath(profile))
		t.Fatalf("signed-out profile saved a session: %v (%s)", err, data)
	}
}
