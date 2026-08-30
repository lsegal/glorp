package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestBrowserSignedOutErrorIdentity pins the two properties callers depend on:
// a signed-out read is matchable as its own category, and it is not the
// extraction failure the screenshot fallback exists to recover. No screenshot
// of a signed-out page can be read into issues, so routing one there would
// spend the vision budget on a page that can never answer.
func TestBrowserSignedOutErrorIdentity(t *testing.T) {
	err := error(&browserSignedOutError{URL: "https://github.com/lsegal/glorp/issues", Profile: "/tmp/profile"})
	if !errors.Is(err, errBrowserSignedOut) {
		t.Fatalf("signed-out error does not match errBrowserSignedOut")
	}
	if errors.Is(err, errBrowserExtraction) {
		t.Fatalf("signed-out error must not be mistaken for an extraction failure")
	}
	if !errors.Is(&browserExtractionError{URL: "u"}, errBrowserExtraction) {
		t.Fatalf("extraction error stopped matching its own sentinel")
	}
	if errors.Is(&browserExtractionError{URL: "u"}, errBrowserSignedOut) {
		t.Fatalf("extraction error must not be mistaken for a signed-out read")
	}
	for _, want := range []string{"https://github.com/lsegal/glorp/issues", "/tmp/profile", "-browser-profile"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("signed-out message %q does not mention %q", err.Error(), want)
		}
	}
	// Without a known profile the message still has to stand on its own.
	bare := (&browserSignedOutError{URL: "https://github.com/lsegal/glorp/issues"}).Error()
	if strings.Contains(bare, "the browser profile at ") {
		t.Fatalf("bare message should not name an empty profile: %q", bare)
	}
}

// TestBrowserSignedOutProbe covers the probe's answers, including the one that
// matters most: an evaluation that fails must not be read as evidence of
// anything, or a broken probe would blame a perfectly good profile.
func TestBrowserSignedOutProbe(t *testing.T) {
	for _, test := range []struct {
		name  string
		state browserSignInState
		fail  bool
		want  bool
	}{
		{name: "signed out", state: browserSignInState{SignedOut: true}, want: true},
		{name: "signed in", state: browserSignInState{SignedIn: true, Login: "lsegal"}},
		{name: "no evidence either way"},
		{name: "probe failed", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := &fakeBrowserPage{signIn: test.state, status: 200}
			if test.fail {
				page.evalErr = errors.New("boom")
			}
			if got := browserSignedOut(page); got != test.want {
				t.Fatalf("browserSignedOut = %v, want %v", got, test.want)
			}
		})
	}
}

// TestBrowserIssueSourceSignedOutEmptyList is the regression for issue #402: a
// page that renders GitHub's own honest empty result for an "@me" filter must
// stop the run with an actionable message rather than being reported as an
// issue list with nothing in it.
func TestBrowserIssueSourceSignedOutEmptyList(t *testing.T) {
	var logs []string
	page := &fakeBrowserPage{
		results: []browserIssueList{{Rows: nil, Recognized: true, Empty: true}},
		signIn:  browserSignInState{SignedOut: true},
	}
	source := newTestIssueSource(page, defaultIssueFilter, false, func(format string, args ...interface{}) {
		logs = append(logs, format)
	})
	source.profile = "/tmp/glorp-profile"
	issues, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err == nil {
		t.Fatalf("signed-out empty list returned %d issue(s) and no error", len(issues))
	}
	if !errors.Is(err, errBrowserSignedOut) {
		t.Fatalf("error %v does not match errBrowserSignedOut", err)
	}
	if !strings.Contains(err.Error(), "/tmp/glorp-profile") {
		t.Fatalf("error %v does not name the profile", err)
	}
	if page.signInEvals != 1 {
		t.Fatalf("sign-in probe ran %d time(s), want exactly 1", page.signInEvals)
	}
	for _, line := range logs {
		if strings.Contains(line, "browser read") {
			t.Fatalf("a signed-out read must not be logged as an issue count: %q", line)
		}
	}
}

// TestBrowserIssueSourceSignedOutUnreadablePage prefers the signed-out
// diagnosis over the extraction one when both could be reported: an extractor
// fix cannot help a profile that is logged out.
func TestBrowserIssueSourceSignedOutUnreadablePage(t *testing.T) {
	page := &fakeBrowserPage{
		results: []browserIssueList{{Recognized: false}},
		signIn:  browserSignInState{SignedOut: true},
	}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	_, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if !errors.Is(err, errBrowserSignedOut) {
		t.Fatalf("error %v, want a signed-out read", err)
	}
	if errors.Is(err, errBrowserExtraction) {
		t.Fatalf("error %v was reported as an extraction failure", err)
	}
}

// TestBrowserIssueSourceSignedInEmptyList keeps the honest empty list honest: a
// signed-in profile watching a repository with nothing ready still reports no
// issues rather than an error.
func TestBrowserIssueSourceSignedInEmptyList(t *testing.T) {
	page := &fakeBrowserPage{
		results: []browserIssueList{{Recognized: true, Empty: true}},
		signIn:  browserSignInState{SignedIn: true, Login: "lsegal"},
	}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	issues, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err != nil {
		t.Fatalf("signed-in empty list: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("got %d issue(s), want none", len(issues))
	}
}

// TestBrowserIssueSourceSkipsProbeWhenRowsWereRead is the cost guarantee: the
// probe is a failure-path diagnosis, so a poll that read issues must not pay
// for it.
func TestBrowserIssueSourceSkipsProbeWhenRowsWereRead(t *testing.T) {
	page := &fakeBrowserPage{results: []browserIssueList{{
		Rows:       []browserIssueRow{{Number: 402, Repository: "lsegal/glorp", Title: "bug", State: "open"}},
		Recognized: true,
	}}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	issues, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != 402 {
		t.Fatalf("got %+v, want issue #402", issues)
	}
	if page.signInEvals != 0 {
		t.Fatalf("sign-in probe ran %d time(s) on a successful read", page.signInEvals)
	}
}

// TestBrowserBoardSignedOutEmptyBoard is issue #402's other face: a board that
// renders an honestly empty result because "@me" matched nobody must be
// reported as a signed-out profile rather than as a board with no ready work.
func TestBrowserBoardSignedOutEmptyBoard(t *testing.T) {
	page := &fakeBoardPage{
		documents: []string{readBoardFixture(t, "project-board-empty.html")},
		status:    200,
		signIn:    browserSignInState{SignedOut: true},
	}
	board, _ := newTestBoard(page)
	board.Profile = "/tmp/glorp-profile"
	_, err := board.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if !errors.Is(err, errBrowserSignedOut) {
		t.Fatalf("error %v, want a signed-out read", err)
	}
	if !strings.Contains(err.Error(), "/tmp/glorp-profile") {
		t.Fatalf("error %v does not name the profile", err)
	}
}

// TestBrowserBoardSignedOutUnrenderedBoard checks the ordering against the
// vision fallback: a board that never renders because the profile is signed
// out is diagnosed, not screenshotted.
func TestBrowserBoardSignedOutUnrenderedBoard(t *testing.T) {
	page := &fakeBoardPage{
		documents: []string{readBoardFixture(t, "project-board-loading.html")},
		status:    200,
		signIn:    browserSignInState{SignedOut: true},
	}
	board, _ := newTestBoard(page)
	board.settleAttempts = 2
	_, err := board.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if !errors.Is(err, errBrowserSignedOut) {
		t.Fatalf("error %v, want a signed-out read", err)
	}
	if errors.Is(err, errBrowserExtraction) {
		t.Fatalf("error %v was reported as an extraction failure", err)
	}
}

// TestBrowserBoardSignedInEmptyBoard keeps an empty board with a signed-in
// profile reporting no items rather than an error.
func TestBrowserBoardSignedInEmptyBoard(t *testing.T) {
	page := &fakeBoardPage{
		documents: []string{readBoardFixture(t, "project-board-empty.html")},
		status:    200,
		signIn:    browserSignInState{SignedIn: true, Login: "lsegal"},
	}
	board, _ := newTestBoard(page)
	issues, err := board.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if err != nil {
		t.Fatalf("signed-in empty board: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("got %d item(s), want none", len(issues))
	}
}
