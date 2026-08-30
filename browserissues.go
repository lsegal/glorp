package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// browserIssuesPageLimit bounds how many pages of the issues list a single tick
// walks. Browser mode polls every few seconds, so following the pager to the
// end of a large backlog would spend the whole interval loading pages nobody
// dispatches from; the first pages hold the issues a poll actually acts on.
const browserIssuesPageLimit = 3

// browserPage is the part of a browser tab the issue source drives. BrowserTab
// satisfies it; tests supply a fake so the extraction logic can be exercised
// without a browser or a network.
type browserPage interface {
	Navigate(url string) error
	Reload() error
	Eval(js string, out any) error
	HTTPStatus() int
}

// browserIssueSource lists a repository's issues by reading its rendered GitHub
// issues page instead of calling the API: one tab per target, reloaded on each
// tick, and one Runtime.evaluate per page load that returns the rows. It spends
// no API quota and no agent tokens, and it never issues a GraphQL query.
//
// It implements IssueSource, so the watch loop needs no structural change to
// use it. Issue bodies, dependencies, and sub-issue state are deliberately not
// scraped here: those are hydrated by targeted REST calls just before dispatch
// (see hydrateIssues).
type browserIssueSource struct {
	// pageFor opens (or returns the already-open) tab for a target.
	pageFor func(target string) (browserPage, error)
	// filter and allIssues mirror GHCLI's, so -filter and -all-issues keep the
	// same meaning whichever transport is in use.
	filter    string
	allIssues bool
	logf      func(string, ...interface{})
	// profile is the browser profile directory, named in the signed-out error
	// so the user is told which profile to sign in rather than being left to
	// guess whether -browser-profile was in play.
	profile string
	// browserHydration is the shared candidate/memo hydration helper. It is
	// embedded so the source's own hydrate, handled, and hydrated fields read
	// and write the one memo the project board reader shares (issue #395).
	*browserHydration
	// vision is the bounded screenshot-to-agent fallback, or nil when
	// -browser-vision was not passed. It is consulted only for the
	// distinguishable extraction failure below, never for an empty or a
	// successful read.
	vision *browserVision

	mu sync.Mutex
	// reported remembers which page URLs have already had an extraction
	// failure logged, so a page whose markup glorp cannot read is reported
	// once rather than on every tick.
	reported map[string]bool
	// lastURL is the URL each target's tab was last pointed at, so an
	// unchanged poll reloads the tab instead of navigating it again.
	lastURL map[string]string
	// lastRows fingerprints the issues each target last yielded, so a tick is
	// logged when the list changes and stays silent when it does not.
	lastRows map[string]string
}

// browserIssueHydrator fills in the fields a rendered issues page does not
// carry. GHCLI satisfies it with targeted REST calls; tests supply a fake that
// counts requests.
type browserIssueHydrator interface {
	HydrateIssue(ctx context.Context, repo string, issue *Issue) error
}

// browserHydratedIssue is the memoized result of one hydration: exactly the
// fields a rendered page cannot carry and the dispatch path needs. Title and
// State are memoized too because the hydrator corrects them, and a cached tick
// must yield the same issue a freshly hydrated one does.
type browserHydratedIssue struct {
	Title        string
	Body         string
	State        string
	CreatedAt    time.Time
	DependsOn    []IssueDependency
	HasSubIssues bool
}

// browserHydration is the candidate selection and memoization both browser
// page readers hydrate through: the repository issues page (issue #381) and
// the Projects v2 board (issue #395). One instance is shared by both, keyed by
// target, so the request budget below is a property of the whole run rather
// than of either reader.
type browserHydration struct {
	// hydrate fetches the dispatch-only fields a rendered page does not
	// render. Nil leaves every extracted issue unhydrated, which is what the
	// extraction-only tests want.
	hydrate browserIssueHydrator
	// handled reports issues this run already owns: work it has in flight or
	// has already completed. Those are not dispatch candidates, so they are
	// never worth a fetch. Nil treats every extracted issue as a candidate.
	handled func(Issue) bool

	mu sync.Mutex
	// hydrated memoizes the hydrated fields per target and per "repo#number",
	// so an issue costs its REST calls once for the life of the run. Entries
	// for issues that are no longer candidates are dropped, which is what
	// makes an issue that leaves and re-enters the candidate set hydrate
	// again.
	hydrated map[string]map[string]browserHydratedIssue
}

// newBrowserHydration builds the shared hydration helper for a run.
func newBrowserHydration(hydrate browserIssueHydrator, handled func(Issue) bool) *browserHydration {
	return &browserHydration{hydrate: hydrate, handled: handled, hydrated: map[string]map[string]browserHydratedIssue{}}
}

// newBrowserIssueSource builds the issue source browser mode polls with,
// reading each target through its own tab of the shared browser.
func newBrowserIssueSource(browser *Browser, hydrate browserIssueHydrator, handled func(Issue) bool, filter string, allIssues bool, vision *browserVision, logf func(string, ...interface{})) *browserIssueSource {
	return &browserIssueSource{
		pageFor: func(target string) (browserPage, error) {
			tab, err := browser.Tab(target)
			if err != nil {
				return nil, err
			}
			return tab, nil
		},
		filter:           filter,
		allIssues:        allIssues,
		vision:           vision,
		logf:             logf,
		profile:          browser.Profile(),
		browserHydration: newBrowserHydration(hydrate, handled),
		reported:         map[string]bool{},
		lastURL:          map[string]string{},
		lastRows:         map[string]string{},
	}
}

// errBrowserExtraction marks the failure of reading an issues page that loaded
// but whose contents glorp did not recognise. Callers match it with errors.Is
// to tell "GitHub rendered something we cannot read" (which usually means the
// browser profile is signed out, or GitHub's markup moved) apart from an
// ordinary navigation or protocol error.
var errBrowserExtraction = errors.New("issue list extraction failed")

// browserExtractionError is the distinguishable error the issue source returns
// for a page it could not read, carrying the URL that produced it.
type browserExtractionError struct {
	URL    string
	Reason string
}

func (e *browserExtractionError) Error() string {
	return fmt.Sprintf("read issue list at %s: %s", e.URL, e.Reason)
}

// Is reports errBrowserExtraction so callers can match the category without
// depending on this concrete type.
func (e *browserExtractionError) Is(target error) bool { return target == errBrowserExtraction }

// browserIssuesURL builds the issues-page URL for a repository, carrying the
// same filter the API path searches with as the page's own ?q= value. The "@me"
// qualifiers are left as they are: the browser is signed in as the user, which
// is exactly who "@me" means to GitHub's own search.
//
// "is:issue" and "state:open" are supplied only as defaults for a filter that
// does not already say what to search for: the default filter opens with both
// qualifiers itself, and repeating them would make the logged URL harder to
// read and would contradict a filter that deliberately asked for something else
// (a "--filter is:pr ..." must not become "is:issue ... is:pr").
func browserIssuesURL(repo, filter string, allIssues bool) string {
	if allIssues {
		filter = ""
	}
	var kind, state bool
	for _, term := range strings.Fields(filter) {
		switch {
		case strings.HasPrefix(term, "state:"), term == "is:open", term == "is:closed", term == "is:merged":
			state = true
		case strings.HasPrefix(term, "is:"), strings.HasPrefix(term, "type:"):
			kind = true
		}
	}
	var terms []string
	if !kind {
		terms = append(terms, "is:issue")
	}
	if !state {
		terms = append(terms, "state:open")
	}
	if filter = strings.TrimSpace(filter); filter != "" {
		terms = append(terms, filter)
	}
	query := strings.Join(terms, " ")
	return "https://github.com/" + repo + "/issues?" + url.Values{"q": {query}}.Encode()
}

// browserIssueRow is one row as the page script reports it.
type browserIssueRow struct {
	Number     int      `json:"number"`
	Repository string   `json:"repository"`
	Title      string   `json:"title"`
	State      string   `json:"state"`
	Labels     []string `json:"labels"`
}

// browserIssueList is the page script's result: the rows it found, whether it
// recognised the page at all, and the next page to follow if there is one.
type browserIssueList struct {
	Rows       []browserIssueRow `json:"rows"`
	Recognized bool              `json:"recognized"`
	Empty      bool              `json:"empty"`
	Next       string            `json:"next"`
}

// ListIssues reads the target repository's issues page and returns the issues
// on it. A page with no results yields an empty slice rather than an error; a
// page that loaded but could not be read yields errBrowserExtraction.
func (s *browserIssueSource) ListIssues(ctx context.Context, target string) ([]Issue, error) {
	parsed, err := parseTarget(target)
	if err != nil {
		return nil, err
	}
	if parsed.repo == "" || parsed.isProject || parsed.isDiscussion {
		return nil, fmt.Errorf("browser mode lists issues for an OWNER/REPO target only, not %q", target)
	}
	page, err := s.pageFor(target)
	if err != nil {
		return nil, err
	}
	next := browserIssuesURL(parsed.repo, s.filter, s.allIssues)
	var issues []Issue
	seen := map[string]bool{}
	for visited := 0; visited < browserIssuesPageLimit && next != ""; visited++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := next
		list, err := s.readPage(target, page, current, visited == 0)
		if err != nil {
			recovered := s.recoverWithVision(ctx, target, page, current, err)
			if len(recovered) == 0 {
				return nil, err
			}
			for _, ref := range recovered {
				key := parsed.repo + "#" + strconv.Itoa(ref.Number)
				if seen[key] {
					continue
				}
				seen[key] = true
				issues = append(issues, issueFromBrowserRow(browserIssueRow{Number: ref.Number}, parsed.repo))
			}
			break
		}
		for _, row := range list.Rows {
			if row.Number <= 0 {
				continue
			}
			key := row.Repository + "#" + strconv.Itoa(row.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			issues = append(issues, issueFromBrowserRow(row, parsed.repo))
		}
		next = nextBrowserIssuesURL(list.Next)
	}
	if err := s.hydrateIssues(ctx, target, issues); err != nil {
		return nil, err
	}
	s.logChange(target, issues)
	return issues, nil
}

// hydrateIssues fills in the body and dependency state a rendered issues page
// or project board does not carry, for the issues that could actually be
// dispatched from this tick.
//
// The request budget this keeps is deliberate and is the whole point of the
// browser transport: hydration is O(new candidate issues), never O(list) and
// never O(ticks). An issue is fetched when it is a dispatch candidate (not
// already in flight and not already completed by this run, per the handled
// predicate the watch loop backs with .glorp.json and its in-flight set) and
// has not been fetched yet; the result is memoized for the life of the run. A
// steady-state tick whose extraction is unchanged therefore makes zero
// requests, and the requests it does make are plain REST GETs -- never a
// GraphQL query.
func (s *browserHydration) hydrateIssues(ctx context.Context, target string, issues []Issue) error {
	if s == nil || s.hydrate == nil {
		return nil
	}
	candidates := make(map[string]bool, len(issues))
	for i := range issues {
		issue := &issues[i]
		// issueKey and the handled predicate both read Target, which the
		// watch loop only stamps after ListIssues returns.
		issue.Target = target
		if s.handled != nil && s.handled(*issue) {
			continue
		}
		repo := issueRepository(target, *issue)
		key := repo + "#" + strconv.Itoa(issue.Number)
		candidates[key] = true
		if cached, ok := s.cachedHydration(target, key); ok {
			applyBrowserHydration(issue, cached)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.hydrate.HydrateIssue(ctx, repo, issue); err != nil {
			return err
		}
		s.storeHydration(target, key, browserHydratedIssue{
			Title:        issue.Title,
			Body:         issue.Body,
			State:        issue.State,
			CreatedAt:    issue.CreatedAt,
			DependsOn:    issue.DependsOn,
			HasSubIssues: issue.HasSubIssues,
		})
	}
	s.pruneHydration(target, candidates)
	return nil
}

// cachedHydration returns a target's memoized hydration for an issue, if it
// still has one.
func (s *browserHydration) cachedHydration(target, key string) (browserHydratedIssue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, ok := s.hydrated[target][key]
	return cached, ok
}

// storeHydration memoizes one issue's hydrated fields for the life of the run.
func (s *browserHydration) storeHydration(target, key string, hydrated browserHydratedIssue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hydrated == nil {
		s.hydrated = map[string]map[string]browserHydratedIssue{}
	}
	if s.hydrated[target] == nil {
		s.hydrated[target] = map[string]browserHydratedIssue{}
	}
	s.hydrated[target][key] = hydrated
}

// pruneHydration forgets the issues that are no longer candidates for this
// target, so one that later re-enters the candidate set is fetched afresh
// rather than dispatched from a stale body.
func (s *browserHydration) pruneHydration(target string, candidates map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.hydrated[target] {
		if !candidates[key] {
			delete(s.hydrated[target], key)
		}
	}
}

// applyBrowserHydration copies a memoized hydration back onto a freshly
// extracted row. Only the fields a page cannot carry are written back, so the
// ones the extraction legitimately owns -- a board item's ProjectStatus and
// ProjectItemID above all -- survive hydration untouched.
func applyBrowserHydration(issue *Issue, hydrated browserHydratedIssue) {
	issue.Body = hydrated.Body
	issue.CreatedAt = hydrated.CreatedAt
	issue.DependsOn = hydrated.DependsOn
	issue.HasSubIssues = hydrated.HasSubIssues
	if hydrated.Title != "" {
		issue.Title = hydrated.Title
	}
	if hydrated.State != "" {
		issue.State = hydrated.State
	}
}

// readPage points the tab at one page of the list and runs the extractor in it.
// The first page of a tick reloads when the tab is already there, so a poll
// that changes nothing costs a reload rather than a fresh navigation.
func (s *browserIssueSource) readPage(target string, page browserPage, pageURL string, first bool) (browserIssueList, error) {
	if err := s.visit(target, page, pageURL, first); err != nil {
		return browserIssueList{}, fmt.Errorf("load issue list at %s: %w", pageURL, err)
	}
	if status := page.HTTPStatus(); status >= 400 {
		return browserIssueList{}, fmt.Errorf("load issue list at %s: GitHub returned HTTP %d", pageURL, status)
	}
	var list browserIssueList
	if err := page.Eval(browserIssueRowsScript, &list); err != nil {
		return browserIssueList{}, fmt.Errorf("read issue list at %s: %w", pageURL, err)
	}
	// A page that yielded no rows is where a signed-out profile hides: with
	// "@me" in the filter GitHub renders a genuine, correctly-empty result, so
	// the extraction succeeds and the run reports "0 issues" on every poll
	// forever instead of saying the one thing that would fix it (issue #402).
	// The probe runs only here, so a read that found issues costs nothing.
	if len(list.Rows) == 0 && browserSignedOut(page) {
		return browserIssueList{}, &browserSignedOutError{URL: pageURL, Profile: s.profile}
	}
	if !list.Recognized {
		return browserIssueList{}, s.extractionFailed(pageURL, "no issue rows and no empty-list marker were found on the page (the page's markup may have changed)")
	}
	return list, nil
}

// visit navigates the tab, or reloads it when the first page of a tick is the
// URL the tab already shows.
func (s *browserIssueSource) visit(target string, page browserPage, pageURL string, first bool) error {
	s.mu.Lock()
	unchanged := first && s.lastURL[target] == pageURL
	if first {
		s.lastURL[target] = pageURL
	}
	s.mu.Unlock()
	if unchanged {
		return page.Reload()
	}
	return page.Navigate(pageURL)
}

// extractionFailed builds the extraction error and logs it the first time a
// given URL produces one, so a persistently unreadable page does not repeat
// itself on every tick of a five-second loop.
func (s *browserIssueSource) extractionFailed(pageURL, reason string) error {
	s.mu.Lock()
	report := !s.reported[pageURL]
	s.reported[pageURL] = true
	s.mu.Unlock()
	if report && s.logf != nil {
		s.logf("could not read the issue list at %s: %s", pageURL, reason)
	}
	return &browserExtractionError{URL: pageURL, Reason: reason}
}

// recoverWithVision asks the screenshot fallback to read the page the DOM
// extractor could not. Only the distinguishable extraction failure qualifies: a
// navigation error, an HTTP error, or an empty-but-recognised list is never
// worth a screenshot. A repository's issues page names one repository, so the
// agent is asked for bare numbers here; the board source asks for qualified
// ones. The fallback answers with issue numbers alone, so the issues it
// recovers carry no title or labels; the dispatch path hydrates them from the
// API, and the extractor is still expected to be fixed in code.
func (s *browserIssueSource) recoverWithVision(ctx context.Context, target string, page browserPage, pageURL string, cause error) []browserVisionRef {
	if s.vision == nil || !errors.Is(cause, errBrowserExtraction) {
		return nil
	}
	shooter, ok := page.(browserScreenshotter)
	if !ok {
		return nil
	}
	return s.vision.Recover(ctx, target, pageURL, cause.Error(), shooter.Screenshot, false)
}

// logChange emits one line when a target's issue list differs from the previous
// tick's, and nothing at all when the reload found the same issues.
func (s *browserIssueSource) logChange(target string, issues []Issue) {
	fingerprint := browserIssuesFingerprint(issues)
	s.mu.Lock()
	previous, had := s.lastRows[target]
	s.lastRows[target] = fingerprint
	s.mu.Unlock()
	if had && previous == fingerprint {
		return
	}
	if s.logf != nil {
		s.logf("browser read %d issue(s) from %s", len(issues), target)
	}
}

// browserIssuesFingerprint reduces a tick's issues to a value that changes
// whenever the list does, including when only a title, state, or label moved.
func browserIssuesFingerprint(issues []Issue) string {
	keys := make([]string, 0, len(issues))
	for _, issue := range issues {
		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, label.Name)
		}
		keys = append(keys, fmt.Sprintf("%s#%d/%s/%s/%s", issue.Repository, issue.Number, issue.State, issue.Title, strings.Join(labels, ",")))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}

// issueFromBrowserRow converts one extracted row into an Issue, defaulting the
// repository to the watched one when the row's link did not carry it.
func issueFromBrowserRow(row browserIssueRow, repo string) Issue {
	issue := Issue{
		Number:     row.Number,
		Repository: row.Repository,
		Title:      strings.TrimSpace(row.Title),
		State:      row.State,
	}
	if issue.Repository == "" {
		issue.Repository = repo
	}
	if issue.State == "" {
		issue.State = "open"
	}
	for _, label := range row.Labels {
		if label = strings.TrimSpace(label); label != "" {
			issue.Labels = append(issue.Labels, IssueLabel{Name: label})
		}
	}
	return issue
}

// nextBrowserIssuesURL accepts the pager's target only when it is another
// github.com issues page, so a mis-read control cannot send the tab somewhere
// unrelated.
func nextBrowserIssuesURL(candidate string) string {
	if candidate == "" {
		return ""
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || !strings.HasSuffix(parsed.Path, "/issues") {
		return ""
	}
	return parsed.String()
}
