package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeBrowserPage stands in for a tab: it records what it was asked to load and
// answers each evaluation with the next canned extraction result, so the issue
// source can be exercised without a browser and without a network.
type fakeBrowserPage struct {
	results   []browserIssueList
	evalErr   error
	navigated []string
	reloads   int
	status    int
	evals     int
	// signIn is the answer the sign-in probe gets. The zero value reports a
	// page that is neither signed in nor signed out, which is what every test
	// that predates the probe wants: the diagnosis stays out of the way.
	signIn      browserSignInState
	signInEvals int
}

func (p *fakeBrowserPage) Navigate(url string) error {
	p.navigated = append(p.navigated, url)
	return nil
}

func (p *fakeBrowserPage) Reload() error {
	p.reloads++
	return nil
}

func (p *fakeBrowserPage) HTTPStatus() int { return p.status }

func (p *fakeBrowserPage) Eval(_ string, out any) error {
	if p.evalErr != nil {
		return p.evalErr
	}
	// The sign-in probe is a separate evaluation with its own result type, and
	// it is deliberately not counted as a page read: the page counters below
	// measure how many pages of the list were walked.
	if state, ok := out.(*browserSignInState); ok {
		p.signInEvals++
		*state = p.signIn
		return nil
	}
	if p.evals >= len(p.results) {
		return fmt.Errorf("unexpected evaluation %d", p.evals+1)
	}
	result := p.results[p.evals]
	p.evals++
	// Round-tripping through JSON mirrors how chromedp decodes the script's
	// value, so a field the script names differently than Go does would fail
	// here rather than silently arriving zeroed.
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out.(*browserIssueList))
}

// newTestIssueSource builds a source reading every target through one fake page.
func newTestIssueSource(page *fakeBrowserPage, filter string, allIssues bool, logf func(string, ...interface{})) *browserIssueSource {
	if page.status == 0 {
		page.status = 200
	}
	return &browserIssueSource{
		pageFor:          func(string) (browserPage, error) { return page, nil },
		filter:           filter,
		allIssues:        allIssues,
		logf:             logf,
		browserHydration: newBrowserHydration(nil, nil),
		reported:         map[string]bool{},
		lastURL:          map[string]string{},
		lastRows:         map[string]string{},
	}
}

func TestBrowserIssuesURL(t *testing.T) {
	for _, test := range []struct {
		name      string
		filter    string
		allIssues bool
		want      string
	}{
		{
			name:   "default filter",
			filter: defaultIssueFilter,
			want:   "https://github.com/lsegal/glorp/issues?q=is%3Aissue+state%3Aopen+is%3Aissue+state%3Aopen+assignee%3A%40me+author%3A%40me",
		},
		{
			name:   "custom filter",
			filter: "label:ready",
			want:   "https://github.com/lsegal/glorp/issues?q=is%3Aissue+state%3Aopen+label%3Aready",
		},
		{
			name:      "all issues drops the filter",
			filter:    defaultIssueFilter,
			allIssues: true,
			want:      "https://github.com/lsegal/glorp/issues?q=is%3Aissue+state%3Aopen",
		},
		{
			name:   "empty filter",
			filter: "",
			want:   "https://github.com/lsegal/glorp/issues?q=is%3Aissue+state%3Aopen",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := browserIssuesURL("lsegal/glorp", test.filter, test.allIssues); got != test.want {
				t.Fatalf("browserIssuesURL = %s, want %s", got, test.want)
			}
		})
	}
}

// TestBrowserIssueSourceListIssues covers the rows-to-issues mapping the
// dispatch path downstream consumes.
func TestBrowserIssueSourceListIssues(t *testing.T) {
	page := &fakeBrowserPage{results: []browserIssueList{{
		Recognized: true,
		Rows: []browserIssueRow{
			{Number: 12, Repository: "lsegal/glorp", Title: " Fix the parser ", State: "open", Labels: []string{"bug", " ", "ready"}},
			{Number: 13, Title: "No repository on the row", State: ""},
			{Number: 0, Title: "Not an issue row"},
		},
	}}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	issues, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2: %+v", len(issues), issues)
	}
	if issues[0].Number != 12 || issues[0].Title != "Fix the parser" || issues[0].Repository != "lsegal/glorp" || issues[0].State != "open" {
		t.Fatalf("first issue %+v", issues[0])
	}
	if len(issues[0].Labels) != 2 || issues[0].Labels[0].Name != "bug" || issues[0].Labels[1].Name != "ready" {
		t.Fatalf("labels %+v, want bug and ready with the blank dropped", issues[0].Labels)
	}
	if issues[1].Repository != "lsegal/glorp" {
		t.Fatalf("repository %q, want the watched repository", issues[1].Repository)
	}
	if issues[1].State != "open" {
		t.Fatalf("state %q, want open by default", issues[1].State)
	}
}

// TestBrowserIssueSourceEmptyList checks a page that genuinely has no results
// is not mistaken for a page that could not be read.
func TestBrowserIssueSourceEmptyList(t *testing.T) {
	page := &fakeBrowserPage{results: []browserIssueList{{Recognized: true, Empty: true}}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	issues, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("got %d issues, want none", len(issues))
	}
}

// TestBrowserIssueSourceExtractionFailure checks an unreadable page produces the
// distinguishable error, and that it is logged once rather than on every tick.
func TestBrowserIssueSourceExtractionFailure(t *testing.T) {
	page := &fakeBrowserPage{results: []browserIssueList{{}, {}, {}}}
	var logged []string
	source := newTestIssueSource(page, defaultIssueFilter, false, func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	for tick := 0; tick < 3; tick++ {
		_, err := source.ListIssues(context.Background(), "lsegal/glorp")
		if !errors.Is(err, errBrowserExtraction) {
			t.Fatalf("tick %d error %v, want errBrowserExtraction", tick, err)
		}
		var extraction *browserExtractionError
		if !errors.As(err, &extraction) {
			t.Fatalf("tick %d error %v does not carry the failing URL", tick, err)
		}
		if !strings.HasPrefix(extraction.URL, "https://github.com/lsegal/glorp/issues?") {
			t.Fatalf("error URL %q", extraction.URL)
		}
	}
	if len(logged) != 1 {
		t.Fatalf("logged %d line(s) over three ticks, want 1: %v", len(logged), logged)
	}
	if !strings.Contains(logged[0], "https://github.com/lsegal/glorp/issues?") {
		t.Fatalf("log line %q does not name the URL", logged[0])
	}
}

// TestBrowserIssueSourceFollowsPagerToTheCap checks pagination stops at the cap
// and does not return the same issue twice.
func TestBrowserIssueSourceFollowsPagerToTheCap(t *testing.T) {
	pageResult := func(number int, next string) browserIssueList {
		return browserIssueList{
			Recognized: true,
			Rows:       []browserIssueRow{{Number: number, Repository: "lsegal/glorp", Title: "Issue", State: "open"}},
			Next:       next,
		}
	}
	page := &fakeBrowserPage{results: []browserIssueList{
		pageResult(1, "https://github.com/lsegal/glorp/issues?page=2"),
		pageResult(2, "https://github.com/lsegal/glorp/issues?page=3"),
		pageResult(1, "https://github.com/lsegal/glorp/issues?page=4"),
	}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	issues, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if page.evals != browserIssuesPageLimit {
		t.Fatalf("read %d pages, want the cap of %d", page.evals, browserIssuesPageLimit)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2 after deduplication: %+v", len(issues), issues)
	}
}

func TestBrowserIssueSourceStopsAtTheLastPage(t *testing.T) {
	page := &fakeBrowserPage{results: []browserIssueList{{
		Recognized: true,
		Rows:       []browserIssueRow{{Number: 1, Repository: "lsegal/glorp", Title: "Issue"}},
	}}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	if _, err := source.ListIssues(context.Background(), "lsegal/glorp"); err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if page.evals != 1 {
		t.Fatalf("read %d pages, want 1 when the page has no pager", page.evals)
	}
}

// TestBrowserIssueSourceReloadsUnchangedTarget checks a repeat tick reloads the
// tab it already has open rather than navigating it somewhere it already is.
func TestBrowserIssueSourceReloadsUnchangedTarget(t *testing.T) {
	page := &fakeBrowserPage{results: []browserIssueList{
		{Recognized: true, Empty: true},
		{Recognized: true, Empty: true},
	}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	for tick := 0; tick < 2; tick++ {
		if _, err := source.ListIssues(context.Background(), "lsegal/glorp"); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
	if len(page.navigated) != 1 {
		t.Fatalf("navigated %d time(s), want 1: %v", len(page.navigated), page.navigated)
	}
	if page.reloads != 1 {
		t.Fatalf("reloaded %d time(s), want 1", page.reloads)
	}
}

// TestBrowserIssueSourceLogsOnlyOnChange checks the five-second loop stays quiet
// while nothing about the list changes.
func TestBrowserIssueSourceLogsOnlyOnChange(t *testing.T) {
	rows := func(titles ...string) browserIssueList {
		list := browserIssueList{Recognized: true}
		for i, title := range titles {
			list.Rows = append(list.Rows, browserIssueRow{Number: i + 1, Repository: "lsegal/glorp", Title: title, State: "open"})
		}
		return list
	}
	page := &fakeBrowserPage{results: []browserIssueList{
		rows("One"),
		rows("One"),
		rows("One", "Two"),
		rows("One", "Two"),
	}}
	var logged []string
	source := newTestIssueSource(page, defaultIssueFilter, false, func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	for tick := 0; tick < 4; tick++ {
		if _, err := source.ListIssues(context.Background(), "lsegal/glorp"); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
	if len(logged) != 2 {
		t.Fatalf("logged %d line(s) over four ticks with one change, want 2: %v", len(logged), logged)
	}
}

func TestBrowserIssueSourceRejectsHTTPErrors(t *testing.T) {
	page := &fakeBrowserPage{status: 404, results: []browserIssueList{{Recognized: true}}}
	source := newTestIssueSource(page, defaultIssueFilter, false, nil)
	_, err := source.ListIssues(context.Background(), "lsegal/glorp")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error %v, want an HTTP 404 failure", err)
	}
	if errors.Is(err, errBrowserExtraction) {
		t.Fatalf("a 404 must not be reported as an extraction failure")
	}
}

func TestBrowserIssueSourceRejectsNonRepositoryTargets(t *testing.T) {
	for _, target := range []string{
		"https://github.com/orgs/lsegal/projects/1",
		"https://github.com/lsegal/glorp/discussions",
	} {
		page := &fakeBrowserPage{}
		source := newTestIssueSource(page, defaultIssueFilter, false, nil)
		if _, err := source.ListIssues(context.Background(), target); err == nil {
			t.Fatalf("target %s was accepted, want an OWNER/REPO-only error", target)
		}
	}
}

// TestNextBrowserIssuesURL checks a mis-read pager cannot send the tab off
// GitHub or off the issues list.
func TestNextBrowserIssuesURL(t *testing.T) {
	for _, test := range []struct {
		candidate string
		want      string
	}{
		{candidate: "https://github.com/lsegal/glorp/issues?page=2", want: "https://github.com/lsegal/glorp/issues?page=2"},
		{candidate: "https://github.com/lsegal/glorp/issues", want: "https://github.com/lsegal/glorp/issues"},
		{candidate: "https://example.com/lsegal/glorp/issues", want: ""},
		{candidate: "http://github.com/lsegal/glorp/issues", want: ""},
		{candidate: "https://github.com/lsegal/glorp/pulls", want: ""},
		{candidate: "", want: ""},
	} {
		if got := nextBrowserIssuesURL(test.candidate); got != test.want {
			t.Fatalf("nextBrowserIssuesURL(%q) = %q, want %q", test.candidate, got, test.want)
		}
	}
}

// TestBrowserIssueSourceImplementsIssueSource keeps the source usable as the
// watch loop's issue source without any structural change to Glorp.
func TestBrowserIssueSourceImplementsIssueSource(t *testing.T) {
	var _ IssueSource = (*browserIssueSource)(nil)
	var _ browserPage = (*BrowserTab)(nil)
}
