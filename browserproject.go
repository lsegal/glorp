package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// BrowserBoard reads a Projects v2 board through its rendered page instead of
// the projects(v2) GraphQL API, which is where the remaining GraphQL calls in
// the watch loop live (issue #276). It satisfies both IssueSource and
// ProjectStateSource for project targets.
//
// Only reads move to the browser. Writing a status back still goes through
// `gh api graphql` in GHCLI.SetIssueStatus: the board page offers no stable
// way to drive a status change, and SetIssueStatus already looks an item's id
// up by issue number when the caller has none, which is exactly the case for
// issues extracted here.
type BrowserBoard struct {
	// Page hands out the tab a target is read in, one tab per target.
	Page func(name string) (browserPage, error)
	// Filter and AllIssues mirror the GHCLI fields of the same name so the
	// board page is asked for the same items the GraphQL query selects.
	Filter    string
	AllIssues bool
	// Vision is the bounded screenshot-to-agent fallback, shared with the
	// repository issue source so the per-run cap is one budget for the whole
	// run. Nil (the default, without -browser-vision) means a board that never
	// renders is simply reported as an extraction failure.
	Vision *browserVision
	// hydration fills in the dispatch-only fields the board page cannot show,
	// through the same candidate/memo helper the repository issues page uses
	// (issue #395). Nil leaves board items unhydrated, which is what the
	// extraction-only tests want.
	hydration *browserHydration
	// Profile is the browser profile directory, named in the signed-out error
	// the same way the repository issues page names it.
	Profile string

	// maxItems caps how many rows are read from one board, matching the cap
	// the GraphQL path applies. maxScrolls caps how many times a virtualized
	// list is scrolled to materialize more rows. settleAttempts and
	// settleDelay bound the wait for the client-rendered board to appear.
	// sleep is a test seam for that wait.
	maxItems       int
	maxScrolls     int
	settleAttempts int
	settleDelay    time.Duration
	sleep          func(context.Context, time.Duration) bool
}

const (
	defaultBoardMaxItems       = 1000
	defaultBoardMaxScrolls     = 40
	defaultBoardSettleAttempts = 20
	defaultBoardSettleDelay    = 250 * time.Millisecond
)

// newBrowserBoard builds the board reader for a run's browser, giving each
// target its own tab so boards are reloaded rather than reopened.
func newBrowserBoard(browser *Browser, filter string, allIssues bool, vision *browserVision, hydration *browserHydration) *BrowserBoard {
	return &BrowserBoard{
		Page: func(name string) (browserPage, error) {
			tab, err := browser.Tab(name)
			if err != nil {
				return nil, err
			}
			return tab, nil
		},
		Filter:    filter,
		AllIssues: allIssues,
		Vision:    vision,
		hydration: hydration,
		Profile:   browser.Profile(),
	}
}

// boardDocumentScript returns the page as it currently stands. Extraction is
// done in Go rather than in the page so the selectors are covered by tests
// against captured markup; a script that returned already-parsed rows could
// only ever be exercised against a live board.
const boardDocumentScript = "document.documentElement.outerHTML"

// boardScrollScript scrolls the board's own scroller to the bottom and reports
// whether it actually moved. Projects v2 virtualizes its rows, so the rows past
// the viewport are not in the DOM until they are scrolled into it. The largest
// element that has more content than it can show is the list; falling back to
// the document covers the layouts that scroll the page itself.
const boardScrollScript = `(function(){
  var best = null, area = 0;
  document.querySelectorAll('*').forEach(function(el){
    if (el.scrollHeight - el.clientHeight > 40) {
      var size = el.clientWidth * el.clientHeight;
      if (size > area) { area = size; best = el; }
    }
  });
  var scroller = best || document.scrollingElement || document.documentElement;
  var before = scroller.scrollTop;
  scroller.scrollTop = scroller.scrollHeight;
  return scroller.scrollTop > before;
})()`

// boardRow is one item as the board page shows it.
type boardRow struct {
	Number     int
	Repository string
	Title      string
	Status     string
}

func (b *BrowserBoard) items() int {
	if b.maxItems > 0 {
		return b.maxItems
	}
	return defaultBoardMaxItems
}

func (b *BrowserBoard) scrolls() int {
	if b.maxScrolls > 0 {
		return b.maxScrolls
	}
	return defaultBoardMaxScrolls
}

func (b *BrowserBoard) attempts() int {
	if b.settleAttempts > 0 {
		return b.settleAttempts
	}
	return defaultBoardSettleAttempts
}

func (b *BrowserBoard) delay() time.Duration {
	if b.settleDelay > 0 {
		return b.settleDelay
	}
	return defaultBoardSettleDelay
}

// pause waits between settle attempts, reporting false when the run is being
// shut down so a cancelled poll stops instead of sitting out the whole wait.
func (b *BrowserBoard) pause(ctx context.Context) bool {
	if b.sleep != nil {
		return b.sleep(ctx, b.delay())
	}
	timer := time.NewTimer(b.delay())
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ListIssues reports the board's issues with the fields the dispatch gate
// reads: number, repository, title, and the Status column. The body and the
// dependency/sub-issue state are not on the board page, so they are hydrated
// afterwards through the shared helper, on the same budget the repository
// issues page keeps: one targeted REST read per newly seen dispatch candidate,
// memoized for the life of the run, never per tick and never for the whole
// list. The Status column the board owns is not part of that hydration and
// survives it.
func (b *BrowserBoard) ListIssues(ctx context.Context, target string) ([]Issue, error) {
	rows, err := b.readRows(ctx, target)
	if err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, Issue{
			Number:        row.Number,
			Repository:    row.Repository,
			Title:         row.Title,
			ProjectStatus: row.Status,
		})
	}
	if err := b.hydration.hydrateIssues(ctx, target, issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// ProjectState fingerprints the board from the same page read a poll uses, so
// the push-mode probe costs one page load instead of a second network path.
func (b *BrowserBoard) ProjectState(ctx context.Context, target string) (string, error) {
	rows, err := b.readRows(ctx, target)
	if err != nil {
		return "", err
	}
	return boardRowsFingerprint(rows), nil
}

// boardRowsFingerprint hashes each item's identity and status, ordered
// independently of how the page laid them out so that a reordered board alone
// never reads as a change. It mirrors projectItemsFingerprint on the API path.
func boardRowsFingerprint(rows []boardRow) string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, fmt.Sprintf("%s#%d\x00%s", row.Repository, row.Number, row.Status))
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// readRows loads a board and extracts every item it shows, scrolling as far as
// it needs to for a virtualized list.
func (b *BrowserBoard) readRows(ctx context.Context, target string) ([]boardRow, error) {
	parsed, err := parseTarget(target)
	if err != nil {
		return nil, err
	}
	if !parsed.isProject {
		return nil, fmt.Errorf("project board extraction requires a project target, got %q", target)
	}
	page, err := b.Page(target)
	if err != nil {
		return nil, err
	}
	if err := page.Navigate(boardURL(parsed, b.Filter, b.AllIssues)); err != nil {
		return nil, fmt.Errorf("open project board %s: %w", target, err)
	}
	if status := page.HTTPStatus(); status >= 400 {
		return nil, fmt.Errorf("open project board %s: HTTP %d", target, status)
	}

	// The board is a client-rendered React app, so the first read after
	// navigation usually lands before it has drawn anything. Wait for the
	// board's own markup rather than for a row, so an empty board is reported
	// immediately instead of costing the whole timeout on every poll.
	var rows []boardRow
	rendered := false
	for attempt := 0; attempt < b.attempts(); attempt++ {
		found, ready, err := b.harvest(page)
		if err != nil {
			return nil, fmt.Errorf("read project board %s: %w", target, err)
		}
		if ready {
			rows, rendered = found, true
			break
		}
		if !b.pause(ctx) {
			return nil, ctx.Err()
		}
	}
	// A board that showed nothing is the other face of issue #402: a
	// signed-out profile makes an "@me" filter match nobody, and a board that
	// renders an honestly empty result is indistinguishable from a board with
	// no ready work. Checking before the vision fallback matters as much as
	// checking at all, because no screenshot of a signed-out page can be read
	// into issues.
	if (!rendered || len(rows) == 0) && browserSignedOut(page) {
		return nil, &browserSignedOutError{URL: boardURL(parsed, b.Filter, b.AllIssues), Profile: b.Profile}
	}
	if !rendered {
		// A board that loaded but never drew its own markup is the same
		// category of failure as an unrecognised issues page: GitHub served
		// something, and glorp could not read it. Reporting it as
		// errBrowserExtraction lets callers tell it apart from a navigation or
		// HTTP failure the way they already can for the issues page.
		failure := &browserExtractionError{
			URL:    boardURL(parsed, b.Filter, b.AllIssues),
			Reason: fmt.Sprintf("the board did not render within %s", time.Duration(b.attempts())*b.delay()),
		}
		if recovered := b.recoverWithVision(ctx, target, page, failure); len(recovered) > 0 {
			return recovered, nil
		}
		return nil, failure
	}

	seen := map[string]bool{}
	collected := make([]boardRow, 0, len(rows))
	appendBoardRows(&collected, seen, rows, b.items())
	for scroll := 0; scroll < b.scrolls() && len(collected) < b.items(); scroll++ {
		moved := false
		if err := page.Eval(boardScrollScript, &moved); err != nil {
			return nil, fmt.Errorf("scroll project board %s: %w", target, err)
		}
		if !moved {
			break
		}
		found, _, err := b.harvest(page)
		if err != nil {
			return nil, fmt.Errorf("read project board %s: %w", target, err)
		}
		if appendBoardRows(&collected, seen, found, b.items()) == 0 {
			break
		}
	}
	return collected, nil
}

// recoverWithVision asks the screenshot fallback to read a board the DOM
// extractor could not, and turns its answer into rows. A board spans
// repositories, so the agent is asked for qualified OWNER/REPO#NUMBER
// references: a bare number could not be turned back into an addressable
// issue. It is asked for each item's Status column alongside the reference,
// because that column is what the --ready-state gate reads: without it a
// recovered board would be addressable but permanently undispatchable (issue
// #398). Recovered rows still carry no title, which nothing gates on. The
// extractor is still expected to be fixed in code.
func (b *BrowserBoard) recoverWithVision(ctx context.Context, target string, page browserPage, cause *browserExtractionError) []boardRow {
	if b.Vision == nil {
		return nil
	}
	shooter, ok := page.(browserScreenshotter)
	if !ok {
		return nil
	}
	refs := b.Vision.Recover(ctx, target, cause.URL, cause.Error(), shooter.Screenshot, true)
	rows := make([]boardRow, 0, len(refs))
	seen := map[string]bool{}
	statusless := 0
	for _, ref := range refs {
		if ref.Repository == "" || ref.Number <= 0 {
			continue
		}
		key := fmt.Sprintf("%s#%d", ref.Repository, ref.Number)
		if seen[key] {
			continue
		}
		seen[key] = true
		if ref.Status == "" {
			statusless++
		}
		rows = append(rows, boardRow{Number: ref.Number, Repository: ref.Repository, Status: ref.Status})
	}
	if statusless > 0 {
		// An item the screenshot showed no status for cannot be dispatched,
		// because the ready-state gate has nothing to match. Saying so is the
		// point: a recovered board that stays quiet must not look like a
		// recovered board with nothing to do.
		b.Vision.log("browser vision: %d of %d recovered item(s) on %s came back with no Status column, so the ready-state gate will not dispatch them; fix the board extractor rather than relying on this", statusless, len(rows), cause.URL)
	}
	return rows
}

// harvest reads the page once, reporting the items it shows and whether the
// board has rendered at all yet.
func (b *BrowserBoard) harvest(page browserPage) ([]boardRow, bool, error) {
	document := ""
	if err := page.Eval(boardDocumentScript, &document); err != nil {
		return nil, false, err
	}
	return parseBoardDocument(document)
}

// appendBoardRows adds the items that have not been seen yet, up to the cap,
// and reports how many were new. Scrolling re-reads rows that were already
// visible, so de-duplication is what makes the scroll loop terminate.
func appendBoardRows(into *[]boardRow, seen map[string]bool, rows []boardRow, limit int) int {
	added := 0
	for _, row := range rows {
		if len(*into) >= limit {
			break
		}
		key := fmt.Sprintf("%s#%d", row.Repository, row.Number)
		if seen[key] {
			continue
		}
		seen[key] = true
		*into = append(*into, row)
		added++
	}
	return added
}

// boardURL builds the board's page URL for any of the three project target
// shapes parseTarget accepts. The table layout is requested explicitly because
// it puts the Status field in a cell of every row, where the board layout only
// implies it through which column a card sits in. The item filter is handed to
// the page rather than applied afterwards, so the browser path selects the same
// items the GraphQL query does.
func boardURL(t target, filter string, allIssues bool) string {
	base := "https://github.com/"
	if t.projectOwnerType != "" {
		base += t.projectOwnerType + "/" + t.owner + "/projects/" + t.projectID
	} else {
		base += t.repo + "/projects/" + t.projectID
	}
	params := url.Values{"layout": {"table"}}
	if query := projectItemQuery(filter, allIssues); query != "" {
		params.Set("filterQuery", query)
	}
	return base + "?" + params.Encode()
}

// issueLinkPattern matches the path of a link to an issue. Pull requests live
// under /pull/ and draft board cards link nowhere at all, so both are dropped
// simply by not matching.
var issueLinkPattern = regexp.MustCompile(`^/([^/?#]+)/([^/?#]+)/issues/(\d+)$`)

// parseBoardDocument extracts the board's items from its markup, reporting
// whether the board rendered. The table layout is read first because that is
// the layout the extractor asks for; the board layout is handled as a fallback
// for a board that ignores the layout parameter or is saved on a board view.
func parseBoardDocument(document string) ([]boardRow, bool, error) {
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return nil, false, fmt.Errorf("parse project board page: %w", err)
	}
	if rows, ok := tableBoardRows(root); ok {
		return rows, true, nil
	}
	if rows, ok := columnBoardRows(root); ok {
		return rows, true, nil
	}
	return nil, false, nil
}

// tableBoardRows reads the table layout: one row per item, with the Status
// field in a cell of its own.
func tableBoardRows(root *html.Node) ([]boardRow, bool) {
	grid := findNode(root, func(n *html.Node) bool {
		return n.Data == "table" || hasRole(n, "table") || hasRole(n, "grid")
	})
	if grid == nil {
		return nil, false
	}
	rows := []boardRow{}
	for _, node := range collectNodes(grid, func(n *html.Node) bool {
		return n.Data == "tr" || hasRole(n, "row")
	}) {
		row, ok := issueRowFromNode(node)
		if !ok {
			continue
		}
		row.Status = statusFromRow(node)
		rows = append(rows, row)
	}
	return rows, true
}

// columnBoardRows reads the board layout, where an item's status is the column
// it sits in rather than a field on the card.
func columnBoardRows(root *html.Node) ([]boardRow, bool) {
	columns := collectNodes(root, isBoardColumn)
	if len(columns) == 0 {
		return nil, false
	}
	rows := []boardRow{}
	for _, column := range columns {
		status := columnStatus(column)
		for _, card := range collectNodes(column, func(n *html.Node) bool {
			return n.Data == "a" && issueLinkPattern.MatchString(linkPath(attrValue(n, "href")))
		}) {
			row, ok := issueRowFromNode(card)
			if !ok {
				continue
			}
			row.Status = status
			rows = append(rows, row)
		}
	}
	return rows, true
}

func isBoardColumn(n *html.Node) bool {
	if containsFold(attrValue(n, "data-testid"), "column") {
		return true
	}
	return hasRole(n, "group") && strings.TrimSpace(attrValue(n, "aria-label")) != ""
}

// columnStatus names the board column, which is the status of every card in it.
func columnStatus(column *html.Node) string {
	if label := strings.TrimSpace(attrValue(column, "aria-label")); label != "" {
		return normalizeStatus(label)
	}
	title := findNode(column, func(n *html.Node) bool {
		return containsFold(attrValue(n, "data-testid"), "column-title") ||
			n.Data == "h1" || n.Data == "h2" || n.Data == "h3" || n.Data == "h4"
	})
	if title == nil {
		return ""
	}
	return normalizeStatus(nodeText(title))
}

// issueRowFromNode reads the issue a row or card refers to. Anything with no
// issue link — a draft card, a pull request, the header row — is not an issue
// and is reported as such.
func issueRowFromNode(node *html.Node) (boardRow, bool) {
	link := node
	if !(node.Data == "a" && issueLinkPattern.MatchString(linkPath(attrValue(node, "href")))) {
		link = findNode(node, func(n *html.Node) bool {
			return n.Data == "a" && issueLinkPattern.MatchString(linkPath(attrValue(n, "href")))
		})
	}
	if link == nil {
		return boardRow{}, false
	}
	parts := issueLinkPattern.FindStringSubmatch(linkPath(attrValue(link, "href")))
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return boardRow{}, false
	}
	title := nodeText(link)
	if title == "" {
		title = strings.TrimSpace(attrValue(link, "aria-label"))
	}
	return boardRow{Number: number, Repository: parts[1] + "/" + parts[2], Title: title}, true
}

// linkPath reduces a link to the path issueLinkPattern matches, so a row that
// uses an absolute github.com URL reads the same as a relative one.
func linkPath(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if parsed.Host != "" && parsed.Host != "github.com" {
		return ""
	}
	return parsed.Path
}

// statusFromRow reads the row's Status cell. The cell is identified by the
// field name GitHub puts in its test id, label, or class rather than by its
// position, because the columns of a board are user-ordered. The innermost
// match wins: an outer container that merely mentions the field would otherwise
// contribute the whole row's text.
func statusFromRow(row *html.Node) string {
	best, bestDepth := "", -1
	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		if n.Type == html.ElementNode && identifiesStatus(n) {
			if text := statusText(n); text != "" && depth > bestDepth {
				best, bestDepth = text, depth
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, depth+1)
		}
	}
	walk(row, 0)
	return best
}

// statusAttributes are the attributes GitHub names a field in. A field's own
// name appears in at least one of them on the cell that holds its value.
var statusAttributes = []string{"data-testid", "data-field-name", "data-field", "aria-label", "class", "id"}

func identifiesStatus(n *html.Node) bool {
	for _, name := range statusAttributes {
		if containsFold(attrValue(n, name), "status") {
			return true
		}
	}
	return false
}

// statusText reads the value out of a status cell, preferring its text and
// falling back to a label for a cell that renders the value as an icon.
func statusText(n *html.Node) string {
	if text := normalizeStatus(nodeText(n)); text != "" {
		return text
	}
	return normalizeStatus(attrValue(n, "aria-label"))
}

// normalizeStatus collapses a cell's whitespace and drops the field name a
// label like "Status: In Progress" repeats, leaving the value on its own.
func normalizeStatus(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 6 && strings.EqualFold(text[:6], "status") {
		// Only when something is left: a column genuinely named "Status"
		// must not normalize away to nothing.
		if rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text[6:]), ":")); rest != "" {
			return rest
		}
	}
	return text
}

// nodeText is the visible text of a node, with scripts and styles left out so
// an inline payload never reads as a field value.
func nodeText(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			if text := strings.TrimSpace(n.Data); text != "" {
				parts = append(parts, text)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func attrValue(n *html.Node, name string) string {
	for _, attr := range n.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func hasRole(n *html.Node, role string) bool {
	return n.Type == html.ElementNode && attrValue(n, "role") == role
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), needle)
}

// findNode returns the first element matching want, in document order.
func findNode(root *html.Node, want func(*html.Node) bool) *html.Node {
	if root.Type == html.ElementNode && want(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findNode(child, want); found != nil {
			return found
		}
	}
	return nil
}

// collectNodes returns the outermost elements matching want. A match is not
// descended into, so a row nested in another row's markup is counted once.
func collectNodes(root *html.Node, want func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n != root && want(n) {
			found = append(found, n)
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return found
}

// browserWatchIssues is browser mode's issue source. The two page readers
// answer for different targets — a repository has an issues page, a project has
// a board — so a run needs both, and each target goes to the one that can read
// it.
type browserWatchIssues struct {
	// Repos reads a repository's rendered issues page (issue #377).
	Repos IssueSource
	// Board reads a project's rendered Projects v2 page (issue #378).
	Board *BrowserBoard
}

func (s browserWatchIssues) ListIssues(ctx context.Context, target string) ([]Issue, error) {
	if isProjectTarget(target) {
		return s.Board.ListIssues(ctx, target)
	}
	return s.Repos.ListIssues(ctx, target)
}
