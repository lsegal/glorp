package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeBoardPage replays captured board markup instead of loading a page, so
// extraction is covered without a browser or a live board. Documents are handed
// out in order and the last one repeats, which is what lets a test model a
// board that renders late or materializes rows as it is scrolled.
type fakeBoardPage struct {
	documents []string
	scrolls   []bool
	status    int
	navErr    error
	evalErr   error

	navigated []string
	reads     int
	scrolled  int
}

func (p *fakeBoardPage) Navigate(url string) error {
	p.navigated = append(p.navigated, url)
	return p.navErr
}

func (p *fakeBoardPage) HTTPStatus() int {
	if p.status == 0 {
		return 200
	}
	return p.status
}

func (p *fakeBoardPage) Eval(_ string, out any) error {
	if p.evalErr != nil {
		return p.evalErr
	}
	switch target := out.(type) {
	case *string:
		index := p.reads
		if index >= len(p.documents) {
			index = len(p.documents) - 1
		}
		p.reads++
		if index < 0 {
			*target = ""
			return nil
		}
		*target = p.documents[index]
	case *bool:
		moved := false
		if p.scrolled < len(p.scrolls) {
			moved = p.scrolls[p.scrolled]
		}
		p.scrolled++
		*target = moved
	default:
		return fmt.Errorf("unexpected eval target %T", out)
	}
	return nil
}

func readBoardFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

// newTestBoard wires a board reader to a fake page and replaces the settle wait
// with a counter, so a test never actually sleeps.
func newTestBoard(page *fakeBoardPage) (*BrowserBoard, *int) {
	waits := 0
	board := &BrowserBoard{
		Page:  func(string) (boardPage, error) { return page, nil },
		sleep: func(context.Context, time.Duration) bool { waits++; return true },
	}
	return board, &waits
}

func boardIssues(t *testing.T, board *BrowserBoard, target string) []Issue {
	t.Helper()
	issues, err := board.ListIssues(context.Background(), target)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	return issues
}

// TestBrowserBoardExtractsTableRows is the main extraction path: the table
// layout, with the Status field in a cell of every row.
func TestBrowserBoardExtractsTableRows(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-table.html")}}
	board, _ := newTestBoard(page)
	issues := boardIssues(t, board, "https://github.com/users/lsegal/projects/3")

	want := []Issue{
		{Number: 378, Repository: "lsegal/glorp", Title: "Browser mode: project board extraction", ProjectStatus: "In Progress"},
		{Number: 42, Repository: "lsegal/zvidlib", Title: "Absolute link issue", ProjectStatus: "Todo"},
		{Number: 377, Repository: "lsegal/glorp", Title: "Browser mode: issue-list extraction", ProjectStatus: "Done"},
		{Number: 276, Repository: "lsegal/glorp", Title: "Remove the remaining GraphQL calls", ProjectStatus: ""},
	}
	if len(issues) != len(want) {
		t.Fatalf("extracted %d issues, want %d: %+v", len(issues), len(want), issues)
	}
	for i, issue := range issues {
		if !reflect.DeepEqual(issue, want[i]) {
			t.Errorf("issue %d = %+v, want %+v", i, issue, want[i])
		}
	}
}

// TestBrowserBoardSkipsNonIssueCards states the exclusion the table fixture
// covers directly: a draft card links to no issue, and a pull request is not
// one, so neither may reach the dispatcher.
func TestBrowserBoardSkipsNonIssueCards(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-table.html")}}
	board, _ := newTestBoard(page)
	for _, issue := range boardIssues(t, board, "https://github.com/users/lsegal/projects/3") {
		if issue.Number == 389 {
			t.Errorf("pull request #389 was extracted as an issue")
		}
		if issue.Title == "Untitled draft" {
			t.Errorf("draft card was extracted as an issue")
		}
	}
}

// TestBrowserBoardExtractsColumnStatus covers the board layout, where an item's
// status is the column it sits in rather than a field on the card.
func TestBrowserBoardExtractsColumnStatus(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-columns.html")}}
	board, _ := newTestBoard(page)
	issues := boardIssues(t, board, "https://github.com/orgs/lsegal/projects/3")

	want := map[int]string{276: "Todo", 378: "In Progress", 42: "Done"}
	if len(issues) != len(want) {
		t.Fatalf("extracted %d issues, want %d: %+v", len(issues), len(want), issues)
	}
	for _, issue := range issues {
		status, ok := want[issue.Number]
		if !ok {
			t.Errorf("unexpected issue #%d on the board", issue.Number)
			continue
		}
		if issue.ProjectStatus != status {
			t.Errorf("issue #%d status %q, want %q", issue.Number, issue.ProjectStatus, status)
		}
	}
}

// TestBrowserBoardEmptyBoardReturnsNoIssues also guards the cost of an empty
// board: it has rendered, so the reader must not sit out the settle wait on
// every poll waiting for a row that is never coming.
func TestBrowserBoardEmptyBoardReturnsNoIssues(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-empty.html")}}
	board, waits := newTestBoard(page)
	issues := boardIssues(t, board, "https://github.com/users/lsegal/projects/3")
	if len(issues) != 0 {
		t.Fatalf("extracted %d issues from an empty board: %+v", len(issues), issues)
	}
	if *waits != 0 {
		t.Errorf("waited %d times for an empty board that had already rendered", *waits)
	}
}

// TestBrowserBoardWaitsForClientRender checks the bounded wait: the first read
// lands on the loading skeleton, and the rows only exist on a later one.
func TestBrowserBoardWaitsForClientRender(t *testing.T) {
	loading := readBoardFixture(t, "project-board-loading.html")
	page := &fakeBoardPage{documents: []string{loading, loading, readBoardFixture(t, "project-board-table.html")}}
	board, waits := newTestBoard(page)
	if issues := boardIssues(t, board, "https://github.com/users/lsegal/projects/3"); len(issues) != 4 {
		t.Fatalf("extracted %d issues after the board rendered, want 4", len(issues))
	}
	if *waits != 2 {
		t.Errorf("waited %d times, want 2 (one per unrendered read)", *waits)
	}
}

func TestBrowserBoardFailsWhenBoardNeverRenders(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-loading.html")}}
	board, _ := newTestBoard(page)
	board.settleAttempts = 3
	_, err := board.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if err == nil || !strings.Contains(err.Error(), "did not render") {
		t.Fatalf("error = %v, want a did-not-render failure", err)
	}
	if page.reads != 3 {
		t.Errorf("read the page %d times, want the 3 attempts it was allowed", page.reads)
	}
}

// virtualizedBoard renders only the rows given to it, which is how the real
// board behaves before its list has been scrolled.
func virtualizedBoard(rows ...string) string {
	return `<html><body><div role="grid">` + strings.Join(rows, "") + `</div></body></html>`
}

func virtualizedRow(number int, status string) string {
	return fmt.Sprintf(`<div role="row"><div role="gridcell" data-testid="TableCell-Title"><a href="/lsegal/glorp/issues/%d">Issue %d</a></div><div role="gridcell" data-testid="TableCell-Status"><span>%s</span></div></div>`, number, number, status)
}

// TestBrowserBoardScrollsVirtualizedRows covers the rows that are not in the
// DOM until the list is scrolled to them, and the de-duplication that keeps the
// rows still on screen from being counted twice.
func TestBrowserBoardScrollsVirtualizedRows(t *testing.T) {
	page := &fakeBoardPage{
		documents: []string{
			virtualizedBoard(virtualizedRow(1, "Todo"), virtualizedRow(2, "Todo")),
			virtualizedBoard(virtualizedRow(2, "Todo"), virtualizedRow(3, "In Progress")),
			virtualizedBoard(virtualizedRow(3, "In Progress"), virtualizedRow(4, "Done")),
		},
		scrolls: []bool{true, true, false},
	}
	board, _ := newTestBoard(page)
	issues := boardIssues(t, board, "https://github.com/users/lsegal/projects/3")
	var numbers []int
	for _, issue := range issues {
		numbers = append(numbers, issue.Number)
	}
	if fmt.Sprint(numbers) != "[1 2 3 4]" {
		t.Fatalf("extracted %v, want every row exactly once in order", numbers)
	}
}

// TestBrowserBoardStopsScrollingAtTheItemCap keeps a very large board from
// scrolling for as long as it takes to reach the end of it.
func TestBrowserBoardStopsScrollingAtTheItemCap(t *testing.T) {
	page := &fakeBoardPage{
		documents: []string{virtualizedBoard(virtualizedRow(1, "Todo"), virtualizedRow(2, "Todo"), virtualizedRow(3, "Todo"))},
		scrolls:   []bool{true, true, true},
	}
	board, _ := newTestBoard(page)
	board.maxItems = 2
	issues := boardIssues(t, board, "https://github.com/users/lsegal/projects/3")
	if len(issues) != 2 {
		t.Fatalf("extracted %d issues, want the 2 the cap allows", len(issues))
	}
	if page.scrolled != 0 {
		t.Errorf("scrolled %d times after the cap was already reached", page.scrolled)
	}
}

func TestBrowserBoardStopsScrollingWhenNoRowsAreLeft(t *testing.T) {
	page := &fakeBoardPage{
		documents: []string{virtualizedBoard(virtualizedRow(1, "Todo"))},
		scrolls:   []bool{true, true, true, true},
	}
	board, _ := newTestBoard(page)
	if issues := boardIssues(t, board, "https://github.com/users/lsegal/projects/3"); len(issues) != 1 {
		t.Fatalf("extracted %d issues, want 1", len(issues))
	}
	if page.scrolled != 1 {
		t.Errorf("scrolled %d times, want to stop after the first pass added nothing", page.scrolled)
	}
}

// TestBoardURL covers all three project target shapes parseTarget accepts, and
// the filter being handed to the page rather than applied after the fact.
func TestBoardURL(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		filter    string
		allIssues bool
		want      string
	}{
		{
			name:   "user project",
			target: "https://github.com/users/lsegal/projects/3",
			want:   "https://github.com/users/lsegal/projects/3?filterQuery=is%3Aissue+is%3Aopen&layout=table",
		},
		{
			name:   "organization project",
			target: "https://github.com/orgs/acme/projects/7",
			want:   "https://github.com/orgs/acme/projects/7?filterQuery=is%3Aissue+is%3Aopen&layout=table",
		},
		{
			name:   "repository-scoped project",
			target: "https://github.com/lsegal/glorp/projects/1",
			want:   "https://github.com/lsegal/glorp/projects/1?filterQuery=is%3Aissue+is%3Aopen&layout=table",
		},
		{
			name:   "custom filter reaches the page",
			target: "https://github.com/users/lsegal/projects/3",
			filter: "is:issue state:open label:bug",
			want:   "https://github.com/users/lsegal/projects/3?filterQuery=is%3Aissue+is%3Aopen+is%3Aissue+state%3Aopen+label%3Abug&layout=table",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseTarget(test.target)
			if err != nil {
				t.Fatalf("parseTarget(%q): %v", test.target, err)
			}
			if got := boardURL(parsed, test.filter, test.allIssues); got != test.want {
				t.Errorf("boardURL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBrowserBoardNavigatesToTheBoardURL(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-empty.html")}}
	board, _ := newTestBoard(page)
	boardIssues(t, board, "https://github.com/lsegal/glorp/projects/1")
	if len(page.navigated) != 1 || !strings.HasPrefix(page.navigated[0], "https://github.com/lsegal/glorp/projects/1?") {
		t.Fatalf("navigated to %v, want the repository-scoped board page once", page.navigated)
	}
}

// TestBrowserBoardProjectStateTracksStatus is the push-mode probe contract: the
// fingerprint changes when a card moves column and holds steady otherwise.
func TestBrowserBoardProjectStateTracksStatus(t *testing.T) {
	const target = "https://github.com/users/lsegal/projects/3"
	fingerprint := func(document string) string {
		board, _ := newTestBoard(&fakeBoardPage{documents: []string{document}})
		state, err := board.ProjectState(context.Background(), target)
		if err != nil {
			t.Fatalf("ProjectState: %v", err)
		}
		return state
	}

	before := fingerprint(virtualizedBoard(virtualizedRow(1, "Todo"), virtualizedRow(2, "Todo")))
	if reordered := fingerprint(virtualizedBoard(virtualizedRow(2, "Todo"), virtualizedRow(1, "Todo"))); reordered != before {
		t.Errorf("fingerprint changed when the board was only reordered")
	}
	if moved := fingerprint(virtualizedBoard(virtualizedRow(1, "In Progress"), virtualizedRow(2, "Todo"))); moved == before {
		t.Errorf("fingerprint held steady after a card changed status")
	}
	if added := fingerprint(virtualizedBoard(virtualizedRow(1, "Todo"), virtualizedRow(2, "Todo"), virtualizedRow(3, "Todo"))); added == before {
		t.Errorf("fingerprint held steady after an item joined the board")
	}
}

func TestBrowserBoardRejectsNonProjectTargets(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-empty.html")}}
	board, _ := newTestBoard(page)
	_, err := board.ListIssues(context.Background(), "lsegal/glorp")
	if err == nil || !strings.Contains(err.Error(), "requires a project target") {
		t.Fatalf("error = %v, want a project-target requirement", err)
	}
	if len(page.navigated) != 0 {
		t.Errorf("navigated to %v for a non-project target", page.navigated)
	}
}

// TestBrowserBoardReportsHTTPFailures keeps a signed-out or missing board from
// reading as a board that simply has no items on it.
func TestBrowserBoardReportsHTTPFailures(t *testing.T) {
	page := &fakeBoardPage{documents: []string{"<html><body>Not found</body></html>"}, status: 404}
	board, _ := newTestBoard(page)
	_, err := board.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error = %v, want the 404 to be reported", err)
	}
}

func TestBrowserBoardReportsNavigationFailures(t *testing.T) {
	page := &fakeBoardPage{documents: []string{""}, navErr: errors.New("tab is gone")}
	board, _ := newTestBoard(page)
	_, err := board.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3")
	if err == nil || !strings.Contains(err.Error(), "tab is gone") {
		t.Fatalf("error = %v, want the navigation failure", err)
	}
}

func TestBrowserBoardStopsOnCancelledContext(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-loading.html")}}
	board := &BrowserBoard{
		Page:  func(string) (boardPage, error) { return page, nil },
		sleep: func(context.Context, time.Duration) bool { return false },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := board.ListIssues(ctx, "https://github.com/users/lsegal/projects/3"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// stubIssueSource records the targets the API path was asked for.
type stubIssueSource struct{ targets []string }

func (s *stubIssueSource) ListIssues(_ context.Context, target string) ([]Issue, error) {
	s.targets = append(s.targets, target)
	return nil, nil
}

// TestBrowserIssueSourceRoutesOnlyProjectTargets pins the scope of browser
// mode's reads: boards come off the page, repositories stay on the API path
// until the issues-page extractor lands.
func TestBrowserIssueSourceRoutesOnlyProjectTargets(t *testing.T) {
	page := &fakeBoardPage{documents: []string{readBoardFixture(t, "project-board-table.html")}}
	board, _ := newTestBoard(page)
	api := &stubIssueSource{}
	source := browserIssueSource{Board: board, API: api}

	if _, err := source.ListIssues(context.Background(), "https://github.com/users/lsegal/projects/3"); err != nil {
		t.Fatalf("ListIssues for a project target: %v", err)
	}
	if len(api.targets) != 0 {
		t.Errorf("project target reached the API path: %v", api.targets)
	}
	if len(page.navigated) != 1 {
		t.Errorf("project target did not reach the board page: %v", page.navigated)
	}

	if _, err := source.ListIssues(context.Background(), "lsegal/glorp"); err != nil {
		t.Fatalf("ListIssues for a repository target: %v", err)
	}
	if len(api.targets) != 1 || api.targets[0] != "lsegal/glorp" {
		t.Errorf("repository target reached %v, want the API path", api.targets)
	}
	if len(page.navigated) != 1 {
		t.Errorf("repository target opened a page: %v", page.navigated)
	}
}

// TestBrowserBoardSatisfiesTheWatchInterfaces is a compile-time check that the
// extractor can be dropped into the watch loop in place of the API path.
func TestBrowserBoardSatisfiesTheWatchInterfaces(t *testing.T) {
	var _ IssueSource = &BrowserBoard{}
	var _ ProjectStateSource = &BrowserBoard{}
	var _ IssueSource = browserIssueSource{}
	var _ boardPage = &BrowserTab{}
}

// TestNormalizeStatus covers the label shapes a Status cell is written in.
func TestNormalizeStatus(t *testing.T) {
	tests := map[string]string{
		"  In   Progress ": "In Progress",
		"Status: Done":     "Done",
		"Status Done":      "Done",
		"Status":           "Status",
		"":                 "",
		"Ready for review": "Ready for review",
	}
	for input, want := range tests {
		if got := normalizeStatus(input); got != want {
			t.Errorf("normalizeStatus(%q) = %q, want %q", input, got, want)
		}
	}
}
