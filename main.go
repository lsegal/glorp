package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"
)

func main() {
	interval := flag.Duration("interval", 30*time.Second, "time between GitHub issue polls")
	concurrency := flag.Int("concurrency", 0, "maximum concurrent agents (0 means 3)")
	agent := flag.String("agent", "codex", "agent to run: codex or claude")
	codexBinary := flag.String("codex-binary", "codex", "Codex executable")
	claudeBinary := flag.String("claude-binary", "claude", "Claude executable")
	statePath := flag.String("state", ".gh-watch.json", "file used to remember handled issue numbers")
	filter := flag.String("filter", "label=agent-ready", "GitHub issue search filter")
	allIssues := flag.Bool("all-issues", false, "disable the default issue filter")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: gh-watch [flags] OWNER/REPO or GitHub URL")
		flag.PrintDefaults()
		os.Exit(2)
	}
	if *interval <= 0 || *concurrency < 0 {
		fmt.Fprintln(os.Stderr, "interval must be positive and concurrency cannot be negative")
		os.Exit(2)
	}
	if *agent != "codex" && *agent != "claude" {
		fmt.Fprintln(os.Stderr, "agent must be codex or claude")
		os.Exit(2)
	}
	limit := *concurrency
	if limit == 0 {
		limit = 3
	}
	binary := *codexBinary
	if *agent == "claude" {
		binary = *claudeBinary
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gh := GHCLI{Binary: "gh"}
	gh.Filter, gh.AllIssues = *filter, *allIssues
	w := &Watcher{Repo: flag.Arg(0), Interval: *interval, Concurrency: limit, StatePath: *statePath, Issues: gh, Labels: gh, Status: gh, Runner: CommandRunner{Binary: binary, Agent: *agent, Repo: flag.Arg(0)}, Out: os.Stdout}
	if err := w.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type GHCLI struct {
	Binary    string
	Filter    string
	AllIssues bool
}

type projectFieldOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type projectField struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Options []projectFieldOption `json:"options"`
}

type projectFields struct {
	Fields []projectField `json:"fields"`
}

type projectView struct {
	ID string `json:"id"`
}

type managedLabel struct {
	name, color, description string
}

var managedLabels = []managedLabel{
	{name: "agent-ready", color: "0E8A16", description: "Issue is ready for an agent"},
	{name: "agent-started", color: "FBCA04", description: "An agent is working on this issue"},
}

func (g GHCLI) EnsureLabels(ctx context.Context, repo string) error {
	target, err := parseTarget(repo)
	if err != nil {
		return err
	}
	if target.isProject {
		return nil
	}
	for _, label := range managedLabels {
		cmd := exec.CommandContext(ctx, g.Binary, "label", "create", label.name, "--repo", target.repo, "--color", label.color, "--description", label.description, "--force")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ensure %s label: %w: %s", label.name, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func (g GHCLI) ListIssues(ctx context.Context, repo string) ([]Issue, error) {
	target, err := parseTarget(repo)
	if err != nil {
		return nil, err
	}
	args := issueListArgs(repo, g.Filter, g.AllIssues)
	if target.isProject {
		args = projectListArgs(target, g.Filter, g.AllIssues)
	}
	cmd := exec.CommandContext(ctx, g.Binary, args...)
	output, err := cmd.CombinedOutput()
	if target.isProject {
		return decodeProjectIssues(output, err)
	}
	return decodeIssues(output, err)
}

func issueListArgs(repo, filter string, allIssues bool) []string {
	target, err := parseTarget(repo)
	if err != nil || target.isProject {
		return nil
	}
	args := []string{"issue", "list", "--repo", target.repo, "--state", "open", "--limit", "1000"}
	if !allIssues && filter != "" {
		args = append(args, "--search", searchQuery(filter))
	}
	return append(args, "--json", "number,title,state,createdAt,labels")
}

type target struct {
	repo, owner, projectID string
	isProject              bool
}

func parseTarget(value string) (target, error) {
	if validRepo(value) {
		return target{repo: value}, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" {
		return target{}, fmt.Errorf("target must be OWNER/REPO or a GitHub repository/project URL")
	}
	parts := strings.Split(strings.Trim(path.Clean(u.Path), "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return target{repo: parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")}, nil
	}
	if len(parts) == 4 && (parts[0] == "users" || parts[0] == "orgs") && parts[2] == "projects" && parts[1] != "" && parts[3] != "" {
		return target{owner: parts[1], projectID: parts[3], isProject: true}, nil
	}
	if len(parts) == 4 && parts[2] == "projects" && parts[0] != "" && parts[1] != "" && parts[3] != "" {
		return target{repo: parts[0] + "/" + parts[1], owner: parts[0], projectID: parts[3], isProject: true}, nil
	}
	return target{}, fmt.Errorf("target must be OWNER/REPO or a GitHub repository/project URL")
}

func projectListArgs(t target, filter string, allIssues bool) []string {
	args := []string{"project", "item-list", t.projectID, "--owner", t.owner, "--format", "json", "--limit", "1000"}
	query := "is:issue is:open"
	if !allIssues && filter != "" {
		query += " " + searchQuery(filter)
	}
	return append(args, "--query", query)
}

func searchQuery(filter string) string {
	terms := strings.Fields(filter)
	for i, term := range terms {
		if strings.HasPrefix(term, "label=") {
			terms[i] = "label:" + strings.TrimPrefix(term, "label=")
		}
	}
	return strings.Join(terms, " ")
}
func decodeIssues(data []byte, err error) ([]Issue, error) {
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	return parseIssues(data)
}

func (g GHCLI) SetIssueLabel(ctx context.Context, repo string, number int, add bool) error {
	action := "--remove-label"
	if add {
		action = "--add-label"
	}
	cmd := exec.CommandContext(ctx, g.Binary, "issue", "edit", fmt.Sprintf("%d", number), "--repo", repo, action, "agent-started")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s agent-started label on issue #%d: %w: %s", strings.TrimPrefix(action, "--"), number, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (g GHCLI) SetIssueStatus(ctx context.Context, repo string, number int, status string) error {
	target, err := parseTarget(repo)
	if err != nil {
		return err
	}
	if !target.isProject {
		return nil
	}

	list := exec.CommandContext(ctx, g.Binary, projectListArgs(target, "", true)...)
	output, err := list.Output()
	items, err := decodeProjectItems(output, err)
	if err != nil {
		return err
	}
	var itemID string
	for _, item := range items {
		if item.Content != nil && item.Content.Number == number {
			itemID = item.ID
			break
		}
	}
	if itemID == "" {
		return fmt.Errorf("issue #%d is not in project %s", number, target.projectID)
	}

	viewCmd := exec.CommandContext(ctx, g.Binary, "project", "view", target.projectID, "--owner", target.owner, "--format", "json")
	viewOutput, err := viewCmd.Output()
	if err != nil {
		return fmt.Errorf("view project: %w", err)
	}
	var view projectView
	if err := json.Unmarshal(viewOutput, &view); err != nil {
		return fmt.Errorf("decode project: %w", err)
	}
	if view.ID == "" {
		return fmt.Errorf("project %s has no ID", target.projectID)
	}

	fieldsCmd := exec.CommandContext(ctx, g.Binary, "project", "field-list", target.projectID, "--owner", target.owner, "--format", "json", "--limit", "1000")
	fieldsOutput, err := fieldsCmd.Output()
	fields, err := decodeProjectFields(fieldsOutput, err)
	if err != nil {
		return err
	}
	var fieldID, optionID string
	for _, field := range fields {
		if field.Name != "Status" {
			continue
		}
		fieldID = field.ID
		for _, option := range field.Options {
			if option.Name == status {
				optionID = option.ID
				break
			}
		}
		break
	}
	if fieldID == "" || optionID == "" {
		return fmt.Errorf("project %s has no Status option %q", target.projectID, status)
	}

	edit := exec.CommandContext(ctx, g.Binary, "project", "item-edit", "--id", itemID, "--field-id", fieldID, "--project-id", view.ID, "--single-select-option-id", optionID)
	if output, err := edit.CombinedOutput(); err != nil {
		return fmt.Errorf("update project status for issue #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	return nil
}

type CommandRunner struct{ Binary, Agent, Repo string }

func commandArgs(r CommandRunner, issue Issue) []string {
	prompt := fmt.Sprintf("/gh-fix %d", issue.Number)
	if r.Agent == "codex" {
		return []string{"exec", "--dangerously-bypass-approvals-and-sandbox", prompt}
	}
	return []string{"-p", "--dangerously-skip-permissions", prompt}
}

func (r CommandRunner) Run(ctx context.Context, issue Issue) error {
	args := commandArgs(r, issue)
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		report, reportErr := bugReportURL(r.Repo, issue, args, output.String())
		if reportErr != nil {
			return fmt.Errorf("agent failed: %w (could not create bug report URL: %v)", err, reportErr)
		}
		return fmt.Errorf("agent failed: %w; bug report: %s", err, report)
	}
	return nil
}

func scrubRobotOutput(_ string) string {
	return "[robot output omitted]"
}

func bugReportURL(repo string, issue Issue, args []string, output string) (string, error) {
	target, err := parseTarget(repo)
	if err != nil || target.isProject || target.repo == "" {
		if err == nil {
			err = fmt.Errorf("bug reports require a repository target")
		}
		return "", err
	}
	values := url.Values{}
	values.Set("template", "bug_report.md")
	values.Set("title", fmt.Sprintf("Agent failed while handling issue #%d", issue.Number))
	values.Set("body", fmt.Sprintf("## Context\n\n- Repository: `%s`\n- Issue: #%d\n- Command: `%s`\n\n## Agent output\n\n%s\n", target.repo, issue.Number, strings.Join(args, " "), scrubRobotOutput(output)))
	return "https://github.com/" + target.repo + "/issues/new?" + values.Encode(), nil
}
func validRepo(repo string) bool {
	parts := strings.Split(repo, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(repo, " \t\r\n")
}
