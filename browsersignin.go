package main

import (
	"errors"
	"fmt"
)

// browserSignInScript reports whether the page glorp is looking at was served
// to a signed-in GitHub session. `glorp auth -status` asks the same question of
// the home page and only needs the login; this one runs on whatever page a poll
// just read, and needs the signed-out answer as its own positive evidence
// rather than as the absence of a login.
//
// Both answers are read as positive evidence rather than as each other's
// negation, because the diagnosis only helps when it is right: GitHub stamps a
// signed-in page with the viewer's login and a `logged-in` body class, and a
// signed-out one with a `logged-out` body class and a link to /login. A page
// carrying neither -- a markup change, or a page glorp was never meant to read
// -- reports neither, and the caller leaves the session out of its explanation
// instead of blaming a profile that is signed in perfectly well.
const browserSignInScript = `(function () {
  var body = document.body;
  var actor = document.querySelector('meta[name="user-login"], meta[name="octolytics-actor-login"]');
  var login = actor ? (actor.getAttribute('content') || '').trim() : '';
  var signedIn = !!login || !!(body && body.classList.contains('logged-in'));
  var signedOut = !!(body && body.classList.contains('logged-out')) ||
    !!document.querySelector('a[href^="/login"], a[href^="https://github.com/login"]');
  return { signedIn: signedIn, signedOut: signedOut && !signedIn, login: login };
})()`

// browserSignInState is browserSignInScript's result.
type browserSignInState struct {
	SignedIn  bool   `json:"signedIn"`
	SignedOut bool   `json:"signedOut"`
	Login     string `json:"login"`
}

// browserSignedOut reports whether a page glorp could not read anything from
// was served to a signed-out browser profile. It is only ever consulted on that
// failure path, so a successful read costs no extra evaluation, and an
// evaluation that fails answers false: a broken probe must not turn a real
// extraction problem into a wrong diagnosis.
func browserSignedOut(page browserPage) bool {
	var state browserSignInState
	if err := page.Eval(browserSignInScript, &state); err != nil {
		return false
	}
	return state.SignedOut
}

// errBrowserSignedOut marks a page that came back with nothing because the
// browser profile is not signed in to GitHub. Callers match it with errors.Is
// to tell it apart from a page whose markup glorp could not read
// (errBrowserExtraction): the two look identical from the outside -- no issues
// -- but only one of them can be fixed by changing an extractor, and neither
// can be fixed by looking at a screenshot of a signed-out page.
var errBrowserSignedOut = errors.New("browser profile is signed out of GitHub")

// browserSignedOutError is the error a signed-out read returns, carrying the
// page that produced it and the profile directory to sign in.
type browserSignedOutError struct {
	URL     string
	Profile string
}

func (e *browserSignedOutError) Error() string {
	where := "the browser profile"
	if e.Profile != "" {
		where = "the browser profile at " + e.Profile
	}
	return fmt.Sprintf("read %s while %s is signed out of GitHub, so \"@me\" in the filter matches nobody and private repositories are invisible: GitHub's own empty result is not glorp's issue list. Run `glorp auth` to sign that profile in (the session persists), or point -browser-profile at a profile that already is", e.URL, where)
}

// Is reports errBrowserSignedOut so callers can match the category without
// depending on this concrete type.
func (e *browserSignedOutError) Is(target error) bool { return target == errBrowserSignedOut }
