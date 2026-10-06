package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CommentKind names which of GitHub's comment APIs a comment belongs to. Each
// has its own IDs, listings, webhook deliveries, and reaction endpoint.
type CommentKind int

const (
	// ConversationComment is an ordinary comment on an issue or on a pull
	// request's conversation tab.
	ConversationComment CommentKind = iota
	// ReviewComment is an inline comment on a pull request's diff (issue
	// #695).
	ReviewComment
	// ReviewBody is the body of a submitted pull request review (issue #695).
	ReviewBody
)

// RecentComment is one comment found by a repository-wide scan of recent
// issue and pull request comments (issue #689), or of pull request review
// comments and review bodies (issue #695).
type RecentComment struct {
	ID int64
	// Kind says which API ID belongs to. The zero value is a conversation
	// comment.
	Kind CommentKind
	// NodeID is the GraphQL node ID, the only handle a review body can be
	// reacted to through.
	NodeID string
	// Number is the issue or pull request the comment was posted on.
	Number    int
	Body      string
	Author    string
	UpdatedAt time.Time
}

// MentionSource is what the poll modes scan for direct @/glorp:ID mentions.
// They receive no issue_comment webhook deliveries, so without it a mention
// posted on an issue or its pull request is never seen (issue #689).
type MentionSource interface {
	// RecentComments lists the comments posted or edited on any issue or
	// pull request in repo since the given time, oldest first.
	RecentComments(ctx context.Context, repo string, since time.Time) ([]RecentComment, error)
	// ClosingIssues names the issues pull request repo#number closes, so a
	// mention posted on a pull request reaches the issue it fixes. A number
	// that is not a pull request closes nothing.
	ClosingIssues(ctx context.Context, repo string, number int) ([]int, error)
}

// ReviewMentionSource is what the poll modes scan for direct mentions posted
// in pull request reviews. Inline review comments and review bodies are
// separate GitHub APIs from the conversation RecentComments reads, so without
// it a mention posted in a review is never seen (issue #695).
type ReviewMentionSource interface {
	// RecentReviewComments lists the inline review comments and submitted
	// review bodies posted or edited on any open pull request in repo since
	// the given time, oldest first.
	RecentReviewComments(ctx context.Context, repo string, since time.Time) ([]RecentComment, error)
}

// ReviewReactor reacts to pull request review comments and review bodies,
// which the issue comment reaction endpoint does not reach (issue #695).
type ReviewReactor interface {
	AddReviewCommentReaction(ctx context.Context, repo string, commentID int64, content string) error
	AddReviewReaction(ctx context.Context, repo string, nodeID string, content string) error
}

// ReviewCommentLister lists the inline review comments and submitted review
// bodies on a pull request, oldest first, so a run in progress hears a
// mention posted in a review of the pull request it opened (issue #695).
type ReviewCommentLister interface {
	ListReviewComments(ctx context.Context, repo string, number int) ([]Comment, error)
}

// mentionScanOverlap is how far each scan reaches back past the newest
// comment the last one saw. GitHub can surface a comment in the listing a
// little after its timestamp, and the overlap costs nothing because comments
// already handled are skipped by ID.
const mentionScanOverlap = time.Minute

// mentionScan is the run loop's memory of the direct mentions it has already
// read. It is only touched from the run loop's own goroutine.
type mentionScan struct {
	start time.Time
	// since is where the next scan of each repository starts.
	since map[string]time.Time
	// handled is every mention comment already acted on, so one comment is
	// reacted to and dispatched at most once however many scans return it.
	// The kind is part of the key because each comment API numbers its own.
	handled map[mentionScanKey]bool
}

type mentionScanKey struct {
	kind CommentKind
	id   int64
}

func newMentionScan(start time.Time) *mentionScan {
	return &mentionScan{start: start, since: make(map[string]time.Time), handled: make(map[mentionScanKey]bool)}
}

// mentionScanRepos lists the repositories a poll scans for mentions: every
// watched repository plus every repository a listed issue came from, which is
// how a project board's repositories are reached.
func mentionScanRepos(targets []string, issues []Issue) []string {
	seen := make(map[string]bool)
	var repos []string
	add := func(repo string) {
		if repo != "" && !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	}
	for _, value := range targets {
		parsed, err := parseTarget(value)
		if err == nil && !parsed.IsProject && !parsed.IsDiscussion {
			add(parsed.Repo)
		}
	}
	for _, issue := range issues {
		add(issue.Repository)
	}
	slices.Sort(repos)
	return repos
}

// scanDirectMentions finds direct mentions of this instance posted since the
// last scan and records each authorized one in directMentions under the
// issue it addresses, the same way the webhook path records an issue_comment
// delivery. A mention on a pull request is recorded under the issues the
// pull request closes. Conversation comments and, when the source can list
// them, review comments and review bodies (issue #695) are scanned from
// cursors of their own, so one listing failing never skips the other's
// window. listed is the "repo#number" keys the poll just listed.
func (w *Glorp) scanDirectMentions(ctx context.Context, scan *mentionScan, repos []string, listed map[string]bool, directMentions map[string]bool) {
	reviews, _ := w.Mentions.(ReviewMentionSource)
	for _, repo := range repos {
		w.scanMentionStream(ctx, scan, repo, repo, "recent comments", w.Mentions.RecentComments, listed, directMentions)
		if reviews != nil {
			w.scanMentionStream(ctx, scan, repo, repo+" reviews", "recent pull request reviews", reviews.RecentReviewComments, listed, directMentions)
		}
	}
}

// scanMentionStream reads one listing of repo's recent comments from the
// cursor stored under cursor and acts on each new mention in it.
func (w *Glorp) scanMentionStream(ctx context.Context, scan *mentionScan, repo, cursor, label string, list func(context.Context, string, time.Time) ([]RecentComment, error), listed map[string]bool, directMentions map[string]bool) {
	since, ok := scan.since[cursor]
	if !ok {
		// An identity is new on every start, so nothing posted before this
		// run can mention it.
		since = scan.start.Add(-mentionScanOverlap)
	}
	comments, err := list(ctx, repo, since)
	if err != nil {
		w.logChanged("mention-scan-"+cursor, err.Error(), "%s failed to scan %s for direct mentions: %v", repo, label, err)
		return
	}
	w.forgetLogged("mention-scan-" + cursor)
	next := since
	for _, comment := range comments {
		if candidate := comment.UpdatedAt.Add(-mentionScanOverlap); candidate.After(next) {
			next = candidate
		}
		key := mentionScanKey{kind: comment.Kind, id: comment.ID}
		if scan.handled[key] || !mentionedIdentity(comment.Body, w.Identity) {
			continue
		}
		if !w.handleScannedMention(ctx, repo, comment, listed, directMentions) {
			// Verification failed rather than ruling the mention out, so the
			// next scan tries it again from where this one started.
			if next.After(comment.UpdatedAt.Add(-mentionScanOverlap)) {
				next = comment.UpdatedAt.Add(-mentionScanOverlap)
			}
			break
		}
		scan.handled[key] = true
	}
	scan.since[cursor] = next
}

// handleScannedMention acts on one scanned mention of this instance, applying
// the same checks as the matching webhook delivery: it reacts with eyes,
// requires the mention to come from an allowed commenter, and records the
// addressed issue in directMentions. A conversation comment must also still
// be the newest comment on its thread. A review comment or review body is
// always on a pull request, so it addresses the issues that pull request
// closes, or nothing. It reports false only when the mention could not be
// verified and should be read again on the next scan.
func (w *Glorp) handleScannedMention(ctx context.Context, repo string, comment RecentComment, listed map[string]bool, directMentions map[string]bool) bool {
	w.reactToMention(ctx, repo, comment.Kind, comment.Number, comment.ID, comment.NodeID)
	authorized := commenterAllowed(comment.Author, w.AllowedCommenters)
	if authorized && w.Comments != nil && comment.Kind == ConversationComment {
		var err error
		authorized, err = authorizedDirectMention(ctx, w.Comments, repo, comment.Number, w.Identity, w.AllowedCommenters)
		if err != nil {
			w.logf("%s#%d failed to verify direct mention of instance %s: %v", repo, comment.Number, w.Identity, err)
			return false
		}
	}
	if !authorized {
		if comment.Kind == ConversationComment {
			w.logf("%s#%d ignoring direct mention of instance %s: not the last comment or not from an allowed commenter", repo, comment.Number, w.Identity)
		} else {
			w.logf("%s#%d ignoring direct mention of instance %s in %s: %s is not an allowed commenter", repo, comment.Number, w.Identity, mentionLocation(comment.Kind), comment.Author)
		}
		return true
	}
	issues := []int{comment.Number}
	if comment.Kind != ConversationComment || !listed[repo+"#"+strconv.Itoa(comment.Number)] {
		closing, err := w.Mentions.ClosingIssues(ctx, repo, comment.Number)
		if err != nil {
			w.logf("%s#%d failed to read which issues it closes: %v", repo, comment.Number, err)
			return false
		}
		if len(closing) > 0 {
			issues = closing
			w.logf("%s pull request #%d directly mentioned instance %s%s; it closes %s", repo, comment.Number, w.Identity, mentionLocationSuffix(comment.Kind), formatIssueRefs(closing))
		} else if comment.Kind != ConversationComment {
			w.logf("%s pull request #%d directly mentioned instance %s in %s, but it closes no issue to run", repo, comment.Number, w.Identity, mentionLocation(comment.Kind))
			return true
		}
	}
	for _, number := range issues {
		directMentions[repo+"#"+strconv.Itoa(number)] = true
		w.logf("issue #%d directly mentioned instance %s; queuing a threaded gh-fix run", number, w.Identity)
	}
	return true
}

// reactToMention acknowledges a mention with the eyes reaction through the
// endpoint its kind of comment needs: a review body is reached only through
// its GraphQL node ID. A failure is logged and otherwise ignored, since the
// reaction only tells a human the mention was read.
func (w *Glorp) reactToMention(ctx context.Context, repo string, kind CommentKind, number int, id int64, nodeID string) {
	var err error
	switch kind {
	case ConversationComment:
		reactor, ok := w.Comments.(CommentReactor)
		if !ok || id == 0 {
			return
		}
		err = reactor.AddReaction(ctx, repo, id, "eyes")
	case ReviewComment:
		reactor, ok := w.Comments.(ReviewReactor)
		if !ok || id == 0 {
			return
		}
		err = reactor.AddReviewCommentReaction(ctx, repo, id, "eyes")
	case ReviewBody:
		reactor, ok := w.Comments.(ReviewReactor)
		if !ok || nodeID == "" {
			return
		}
		err = reactor.AddReviewReaction(ctx, repo, nodeID, "eyes")
	}
	if err != nil {
		w.logf("%s#%d failed to react to mention of instance %s%s: %v", repo, number, w.Identity, mentionLocationSuffix(kind), err)
	}
}

// mentionLocation names where a comment of kind was posted, for log lines.
func mentionLocation(kind CommentKind) string {
	switch kind {
	case ReviewComment:
		return "a review comment"
	case ReviewBody:
		return "a review"
	default:
		return "a comment"
	}
}

// mentionLocationSuffix is mentionLocation as a phrase appended to a log line
// about a mention, empty for an ordinary conversation comment.
func mentionLocationSuffix(kind CommentKind) string {
	if kind == ConversationComment {
		return ""
	}
	return " in " + mentionLocation(kind)
}

// reviewMentionOnGitHub reports whether pull request repo#number carries a
// review comment or review body from author with exactly body, so a review
// delivery's mention is reverified against GitHub rather than trusted from
// the payload (issue #695). A review has no "last comment" the way a
// conversation does, so being on GitHub as delivered is what is checked.
func reviewMentionOnGitHub(ctx context.Context, lister ReviewCommentLister, repo string, number int, body, author string) (bool, error) {
	comments, err := lister.ListReviewComments(ctx, repo, number)
	if err != nil {
		return false, err
	}
	for _, comment := range comments {
		if comment.Body == body && strings.EqualFold(comment.Author, author) {
			return true, nil
		}
	}
	return false, nil
}

func formatIssueRefs(numbers []int) string {
	refs := make([]string, len(numbers))
	for i, number := range numbers {
		refs[i] = "#" + strconv.Itoa(number)
	}
	return strings.Join(refs, ", ")
}

// closingReferencePattern matches a GitHub closing keyword followed by an
// issue reference, optionally qualified with its OWNER/REPO.
var closingReferencePattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+([\w.-]+/[\w.-]+)?#([1-9][0-9]*)\b`)

// closingIssueNumbers lists the repo issues body closes, in first appearance
// order. A reference qualified with another repository is not repo's issue.
func closingIssueNumbers(body, repo string) []int {
	var numbers []int
	seen := make(map[int]bool)
	for _, match := range closingReferencePattern.FindAllStringSubmatch(body, -1) {
		if match[1] != "" && !strings.EqualFold(match[1], repo) {
			continue
		}
		number, err := strconv.Atoi(match[2])
		if err != nil || seen[number] {
			continue
		}
		seen[number] = true
		numbers = append(numbers, number)
	}
	return numbers
}

// RecentComments lists repo's issue and pull request comments updated since
// the given time through the repository-wide comment listing, so a whole
// repository costs one request per scan however many issues it has.
func (g GHCLI) RecentComments(ctx context.Context, repo string, since time.Time) ([]RecentComment, error) {
	query := url.Values{}
	query.Set("since", since.UTC().Format(time.RFC3339))
	query.Set("sort", "updated")
	query.Set("direction", "asc")
	query.Set("per_page", "100")
	output, err := g.apiGETPaginated(ctx, g.isPublicRepo(ctx, repo), "repos/"+repo+"/issues/comments?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("list recent comments in %s: %w: %s", repo, err, strings.TrimSpace(string(output)))
	}
	var raw []struct {
		ID        int64     `json:"id"`
		Body      string    `json:"body"`
		IssueURL  string    `json:"issue_url"`
		UpdatedAt time.Time `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("decode recent comments in %s: %w", repo, err)
	}
	comments := make([]RecentComment, 0, len(raw))
	for _, comment := range raw {
		number, err := strconv.Atoi(comment.IssueURL[strings.LastIndex(comment.IssueURL, "/")+1:])
		if err != nil {
			continue
		}
		comments = append(comments, RecentComment{ID: comment.ID, Number: number, Body: comment.Body, Author: comment.User.Login, UpdatedAt: comment.UpdatedAt})
	}
	return comments, nil
}

// ClosingIssues reads repo#number and, when it is a pull request, returns the
// issues its description closes.
func (g GHCLI) ClosingIssues(ctx context.Context, repo string, number int) ([]int, error) {
	output, err := g.apiGET(ctx, g.isPublicRepo(ctx, repo), "repos/"+repo+"/issues/"+strconv.Itoa(number))
	if err != nil {
		return nil, fmt.Errorf("read #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	var issue struct {
		Body        string           `json:"body"`
		PullRequest *json.RawMessage `json:"pull_request"`
	}
	if err := json.Unmarshal(output, &issue); err != nil {
		return nil, fmt.Errorf("decode #%d: %w", number, err)
	}
	if issue.PullRequest == nil {
		return nil, nil
	}
	return closingIssueNumbers(issue.Body, repo), nil
}

// RecentReviewComments lists the inline review comments and submitted review
// bodies posted on repo's open pull requests since the given time (issue
// #695). Inline comments have a repository-wide listing like conversation
// comments do. Reviews have none, so they are read from each open pull
// request updated since then, which submitting a review counts as.
func (g GHCLI) RecentReviewComments(ctx context.Context, repo string, since time.Time) ([]RecentComment, error) {
	public := g.isPublicRepo(ctx, repo)
	query := url.Values{}
	query.Set("since", since.UTC().Format(time.RFC3339))
	query.Set("sort", "updated")
	query.Set("direction", "asc")
	query.Set("per_page", "100")
	output, err := g.apiGETPaginated(ctx, public, "repos/"+repo+"/pulls/comments?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("list recent review comments in %s: %w: %s", repo, err, strings.TrimSpace(string(output)))
	}
	var raw []struct {
		ID             int64     `json:"id"`
		NodeID         string    `json:"node_id"`
		Body           string    `json:"body"`
		PullRequestURL string    `json:"pull_request_url"`
		UpdatedAt      time.Time `json:"updated_at"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("decode recent review comments in %s: %w", repo, err)
	}
	comments := make([]RecentComment, 0, len(raw))
	for _, comment := range raw {
		number, err := strconv.Atoi(comment.PullRequestURL[strings.LastIndex(comment.PullRequestURL, "/")+1:])
		if err != nil {
			continue
		}
		comments = append(comments, RecentComment{ID: comment.ID, Kind: ReviewComment, NodeID: comment.NodeID, Number: number, Body: comment.Body, Author: comment.User.Login, UpdatedAt: comment.UpdatedAt})
	}
	query = url.Values{}
	query.Set("state", "open")
	query.Set("sort", "updated")
	query.Set("direction", "desc")
	query.Set("per_page", "100")
	output, err = g.apiGET(ctx, public, "repos/"+repo+"/pulls?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("list recently updated pull requests in %s: %w: %s", repo, err, strings.TrimSpace(string(output)))
	}
	var pulls []struct {
		Number    int       `json:"number"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(output, &pulls); err != nil {
		return nil, fmt.Errorf("decode recently updated pull requests in %s: %w", repo, err)
	}
	for _, pull := range pulls {
		if pull.UpdatedAt.Before(since) {
			// The listing is newest first, so no later pull request has a
			// review submitted since the cursor.
			break
		}
		reviews, err := g.pullRequestReviews(ctx, public, repo, pull.Number)
		if err != nil {
			return nil, err
		}
		for _, review := range reviews {
			if review.SubmittedAt.Before(since) {
				continue
			}
			comments = append(comments, RecentComment{ID: review.ID, Kind: ReviewBody, NodeID: review.NodeID, Number: pull.Number, Body: review.Body, Author: review.User.Login, UpdatedAt: review.SubmittedAt})
		}
	}
	slices.SortStableFunc(comments, func(a, b RecentComment) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	return comments, nil
}

// ListReviewComments lists the inline review comments and submitted review
// bodies on pull request repo#number, oldest first (issue #695).
func (g GHCLI) ListReviewComments(ctx context.Context, repo string, number int) ([]Comment, error) {
	public := g.isPublicRepo(ctx, repo)
	output, err := g.apiGETPaginated(ctx, public, "repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return nil, fmt.Errorf("list review comments on #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	var raw []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("decode review comments on #%d: %w", number, err)
	}
	comments := make([]Comment, 0, len(raw))
	for _, comment := range raw {
		comments = append(comments, Comment{Body: comment.Body, Author: comment.User.Login, CreatedAt: comment.CreatedAt})
	}
	reviews, err := g.pullRequestReviews(ctx, public, repo, number)
	if err != nil {
		return nil, err
	}
	for _, review := range reviews {
		comments = append(comments, Comment{Body: review.Body, Author: review.User.Login, CreatedAt: review.SubmittedAt})
	}
	slices.SortStableFunc(comments, func(a, b Comment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return comments, nil
}

type pullRequestReview struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
}

// pullRequestReviews lists the submitted reviews on repo#number that carry a
// body. A pending review is visible only to its author, and a review with no
// body says nothing a mention could be in.
func (g GHCLI) pullRequestReviews(ctx context.Context, public bool, repo string, number int) ([]pullRequestReview, error) {
	output, err := g.apiGETPaginated(ctx, public, "repos/"+repo+"/pulls/"+strconv.Itoa(number)+"/reviews?per_page=100")
	if err != nil {
		return nil, fmt.Errorf("list reviews on #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	var raw []pullRequestReview
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("decode reviews on #%d: %w", number, err)
	}
	reviews := raw[:0]
	for _, review := range raw {
		if strings.EqualFold(review.State, "PENDING") || review.SubmittedAt.IsZero() || strings.TrimSpace(review.Body) == "" {
			continue
		}
		reviews = append(reviews, review)
	}
	return reviews, nil
}

// AddReviewCommentReaction adds an emoji reaction to an inline pull request
// review comment, acknowledging a direct mention posted in one (issue #695).
func (g GHCLI) AddReviewCommentReaction(ctx context.Context, repo string, commentID int64, content string) error {
	output, err := g.run(ctx, "api", "repos/"+repo+"/pulls/comments/"+strconv.FormatInt(commentID, 10)+"/reactions", "-f", "content="+content)
	if err != nil {
		return fmt.Errorf("react to review comment %d: %w: %s", commentID, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// addReactionMutation reacts to any reactable GraphQL node. The REST API has
// no reaction endpoint for a pull request review's body, so this is the only
// way to acknowledge a mention posted in one.
const addReactionMutation = `mutation($subject: ID!, $content: ReactionContent!) { addReaction(input: {subjectId: $subject, content: $content}) { reaction { content } } }`

// AddReviewReaction adds an emoji reaction to the body of a submitted pull
// request review, named by its GraphQL node ID (issue #695). content uses the
// REST spelling, such as "eyes".
func (g GHCLI) AddReviewReaction(ctx context.Context, repo string, nodeID string, content string) error {
	output, err := g.run(ctx, "api", "graphql", "-f", "query="+addReactionMutation, "-f", "subject="+nodeID, "-f", "content="+strings.ToUpper(content))
	if err != nil {
		return fmt.Errorf("react to review %s in %s: %w: %s", nodeID, repo, err, strings.TrimSpace(string(output)))
	}
	return nil
}
