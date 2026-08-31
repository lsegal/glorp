package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCookieJar stands in for a browser's cookie store.
type fakeCookieJar struct {
	jar       []sessionCookie
	written   []sessionCookie
	writes    int
	readErr   error
	writeErr  error
	readCalls int
}

func (f *fakeCookieJar) readCookies() ([]sessionCookie, error) {
	f.readCalls++
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.jar, nil
}

func (f *fakeCookieJar) writeCookies(cookies []sessionCookie) error {
	f.writes++
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = cookies
	return nil
}

func githubSessionCookies() []sessionCookie {
	return []sessionCookie{
		{Name: "user_session", Value: "abc", Domain: "github.com", Path: "/", Secure: true, HTTPOnly: true, SameSite: "Lax", session: true},
		{Name: "logged_in", Value: "yes", Domain: ".github.com", Path: "/"},
		{Name: "dotcom_user", Value: "octocat", Domain: "github.com", Path: "/", session: true},
	}
}

func TestSaveSessionCookiesKeepsOnlySessionCookies(t *testing.T) {
	profile := t.TempDir()
	jar := &fakeCookieJar{jar: githubSessionCookies()}
	if err := saveSessionCookies(profile, jar); err != nil {
		t.Fatalf("saveSessionCookies: %v", err)
	}
	data, err := os.ReadFile(sessionCookiePath(profile))
	if err != nil {
		t.Fatalf("read saved cookies: %v", err)
	}
	var saved []sessionCookie
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode saved cookies: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %d cookies, want the 2 session ones: %+v", len(saved), saved)
	}
	if saved[0].Name != "user_session" || saved[1].Name != "dotcom_user" {
		t.Fatalf("saved the wrong cookies: %+v", saved)
	}
	if saved[0].Value != "abc" || !saved[0].Secure || !saved[0].HTTPOnly || saved[0].SameSite != "Lax" {
		t.Fatalf("cookie attributes were not carried: %+v", saved[0])
	}
}

func TestSaveSessionCookiesWritesProfileOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits to report: Go writes the file
		// with the directory's inherited ACL and stats it as 0666.
		t.Skip("file modes are not enforced on Windows")
	}
	profile := t.TempDir()
	if err := saveSessionCookies(profile, &fakeCookieJar{jar: githubSessionCookies()}); err != nil {
		t.Fatalf("saveSessionCookies: %v", err)
	}
	info, err := os.Stat(sessionCookiePath(profile))
	if err != nil {
		t.Fatalf("stat saved cookies: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("saved cookies with mode %v, want 0600: a live GitHub session must not be world readable", mode)
	}
}

func TestSaveSessionCookiesClearsFileWhenSignedOut(t *testing.T) {
	profile := t.TempDir()
	path := sessionCookiePath(profile)
	if err := os.WriteFile(path, []byte(`[{"name":"user_session"}]`), 0o600); err != nil {
		t.Fatalf("seed saved cookies: %v", err)
	}
	// Only persistent crumbs left: the profile was signed out.
	jar := &fakeCookieJar{jar: []sessionCookie{{Name: "logged_in", Domain: ".github.com"}}}
	if err := saveSessionCookies(profile, jar); err != nil {
		t.Fatalf("saveSessionCookies: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale sign-in survived a signed-out save: %v", err)
	}
}

func TestSaveSessionCookiesWithoutProfileDoesNothing(t *testing.T) {
	jar := &fakeCookieJar{jar: githubSessionCookies()}
	if err := saveSessionCookies("", jar); err != nil {
		t.Fatalf("saveSessionCookies: %v", err)
	}
	if jar.readCalls != 0 {
		t.Fatalf("read the jar %d times for a browser with no profile, want 0", jar.readCalls)
	}
}

func TestSaveSessionCookiesReportsReadFailure(t *testing.T) {
	profile := t.TempDir()
	jar := &fakeCookieJar{readErr: errors.New("browser is closed")}
	err := saveSessionCookies(profile, jar)
	if err == nil || !strings.Contains(err.Error(), "browser is closed") {
		t.Fatalf("saveSessionCookies error = %v, want the jar's failure", err)
	}
	if _, statErr := os.Stat(sessionCookiePath(profile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("wrote a cookie file from a failed read: %v", statErr)
	}
}

func TestRestoreSessionCookiesRoundTrips(t *testing.T) {
	profile := t.TempDir()
	source := &fakeCookieJar{jar: githubSessionCookies()}
	if err := saveSessionCookies(profile, source); err != nil {
		t.Fatalf("saveSessionCookies: %v", err)
	}
	// A new browser process starts with an empty jar, as Chrome does.
	target := &fakeCookieJar{}
	if err := restoreSessionCookies(profile, target); err != nil {
		t.Fatalf("restoreSessionCookies: %v", err)
	}
	if len(target.written) != 2 {
		t.Fatalf("restored %d cookies, want 2: %+v", len(target.written), target.written)
	}
	if target.written[0].Name != "user_session" || target.written[0].Value != "abc" {
		t.Fatalf("restored the wrong cookie: %+v", target.written[0])
	}
	if !target.written[0].Secure || !target.written[0].HTTPOnly || target.written[0].Path != "/" {
		t.Fatalf("restored cookie lost its attributes: %+v", target.written[0])
	}
}

func TestRestoreSessionCookiesWithoutFileIsNotAnError(t *testing.T) {
	target := &fakeCookieJar{}
	if err := restoreSessionCookies(t.TempDir(), target); err != nil {
		t.Fatalf("restoreSessionCookies on a never-signed-in profile: %v", err)
	}
	if target.writes != 0 {
		t.Fatalf("wrote cookies %d times with nothing saved, want 0", target.writes)
	}
}

func TestRestoreSessionCookiesWithEmptyFileWritesNothing(t *testing.T) {
	profile := t.TempDir()
	if err := os.WriteFile(sessionCookiePath(profile), []byte(`[]`), 0o600); err != nil {
		t.Fatalf("seed saved cookies: %v", err)
	}
	target := &fakeCookieJar{}
	if err := restoreSessionCookies(profile, target); err != nil {
		t.Fatalf("restoreSessionCookies: %v", err)
	}
	if target.writes != 0 {
		t.Fatalf("wrote cookies %d times for an empty file, want 0", target.writes)
	}
}

func TestRestoreSessionCookiesDropsAnUnreadableFile(t *testing.T) {
	profile := t.TempDir()
	path := sessionCookiePath(profile)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed saved cookies: %v", err)
	}
	if err := restoreSessionCookies(profile, &fakeCookieJar{}); err == nil {
		t.Fatal("restoreSessionCookies accepted a corrupt file")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt file survived and would fail every later launch: %v", err)
	}
}

func TestRestoreSessionCookiesReportsWriteFailure(t *testing.T) {
	profile := t.TempDir()
	if err := saveSessionCookies(profile, &fakeCookieJar{jar: githubSessionCookies()}); err != nil {
		t.Fatalf("saveSessionCookies: %v", err)
	}
	err := restoreSessionCookies(profile, &fakeCookieJar{writeErr: errors.New("browser is closed")})
	if err == nil || !strings.Contains(err.Error(), "browser is closed") {
		t.Fatalf("restoreSessionCookies error = %v, want the jar's failure", err)
	}
}

func TestSessionCookiePathLivesInTheProfile(t *testing.T) {
	if got, want := sessionCookiePath("/tmp/profile"), filepath.Join("/tmp/profile", sessionCookieFileName); got != want {
		t.Fatalf("sessionCookiePath = %q, want %q", got, want)
	}
	// Two profiles must not share a sign-in: -browser-profile picks which one.
	if sessionCookiePath("/tmp/a") == sessionCookiePath("/tmp/b") {
		t.Fatal("separate profiles share one saved sign-in")
	}
}

// TestBrowserSessionSurvivesAProcessRestart is the issue itself: sign in once,
// stop the browser, start another one on the same profile, and the second
// process must come up holding the sign-in rather than needing `glorp auth`.
func TestBrowserSessionSurvivesAProcessRestart(t *testing.T) {
	profile := t.TempDir()
	signedIn := &fakeCookieJar{jar: githubSessionCookies()}
	if err := saveSessionCookies(profile, signedIn); err != nil {
		t.Fatalf("save on browser close: %v", err)
	}
	next := &fakeCookieJar{}
	if err := restoreSessionCookies(profile, next); err != nil {
		t.Fatalf("restore on browser start: %v", err)
	}
	var names []string
	for _, cookie := range next.written {
		names = append(names, cookie.Name)
	}
	if strings.Join(names, ",") != "user_session,dotcom_user" {
		t.Fatalf("second browser process came up with cookies %v, want the sign-in", names)
	}
}
