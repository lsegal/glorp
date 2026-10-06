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

// RecentComment is one comment found by a repository-wide scan of recent
// issue and pull request comments (issue #689).
type RecentComment struct {
	ID int64
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
	handled map[int64]bool
}

func newMentionScan(start time.Time) *mentionScan {
	return &mentionScan{start: start, since: make(map[string]time.Time), handled: make(map[int64]bool)}
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
// pull request closes. listed is the "repo#number" keys the poll just
// listed.
func (w *Glorp) scanDirectMentions(ctx context.Context, scan *mentionScan, repos []string, listed map[string]bool, directMentions map[string]bool) {
	for _, repo := range repos {
		since, ok := scan.since[repo]
		if !ok {
			// An identity is new on every start, so nothing posted before this
			// run can mention it.
			since = scan.start.Add(-mentionScanOverlap)
		}
		comments, err := w.Mentions.RecentComments(ctx, repo, since)
		if err != nil {
			w.logChanged("mention-scan-"+repo, err.Error(), "%s failed to scan recent comments for direct mentions: %v", repo, err)
			continue
		}
		w.forgetLogged("mention-scan-" + repo)
		next := since
		for _, comment := range comments {
			if candidate := comment.UpdatedAt.Add(-mentionScanOverlap); candidate.After(next) {
				next = candidate
			}
			if scan.handled[comment.ID] || !mentionedIdentity(comment.Body, w.Identity) {
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
			scan.handled[comment.ID] = true
		}
		scan.since[repo] = next
	}
}

// handleScannedMention acts on one scanned mention of this instance, applying
// the same checks as an issue_comment delivery: it reacts with eyes, requires
// the mention to be the newest comment from an allowed commenter, and records
// the addressed issue in directMentions. It reports false only when the
// mention could not be verified and should be read again on the next scan.
func (w *Glorp) handleScannedMention(ctx context.Context, repo string, comment RecentComment, listed map[string]bool, directMentions map[string]bool) bool {
	if reactor, ok := w.Comments.(CommentReactor); ok && comment.ID != 0 {
		if err := reactor.AddReaction(ctx, repo, comment.ID, "eyes"); err != nil {
			w.logf("%s#%d failed to react to mention of instance %s: %v", repo, comment.Number, w.Identity, err)
		}
	}
	authorized := commenterAllowed(comment.Author, w.AllowedCommenters)
	if authorized && w.Comments != nil {
		var err error
		authorized, err = authorizedDirectMention(ctx, w.Comments, repo, comment.Number, w.Identity, w.AllowedCommenters)
		if err != nil {
			w.logf("%s#%d failed to verify direct mention of instance %s: %v", repo, comment.Number, w.Identity, err)
			return false
		}
	}
	if !authorized {
		w.logf("%s#%d ignoring direct mention of instance %s: not the last comment or not from an allowed commenter", repo, comment.Number, w.Identity)
		return true
	}
	issues := []int{comment.Number}
	if !listed[repo+"#"+strconv.Itoa(comment.Number)] {
		closing, err := w.Mentions.ClosingIssues(ctx, repo, comment.Number)
		if err != nil {
			w.logf("%s#%d failed to read which issues it closes: %v", repo, comment.Number, err)
			return false
		}
		if len(closing) > 0 {
			issues = closing
			w.logf("%s pull request #%d directly mentioned instance %s; it closes %s", repo, comment.Number, w.Identity, formatIssueRefs(closing))
		}
	}
	for _, number := range issues {
		directMentions[repo+"#"+strconv.Itoa(number)] = true
		w.logf("issue #%d directly mentioned instance %s; queuing a threaded gh-fix run", number, w.Identity)
	}
	return true
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
