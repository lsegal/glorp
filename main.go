package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
		fmt.Fprintln(os.Stderr, "usage: gh-watch [flags] OWNER/REPO")
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
	w := &Watcher{Repo: flag.Arg(0), Interval: *interval, Concurrency: limit, StatePath: *statePath, Issues: gh, Labels: gh, Runner: CommandRunner{Binary: binary, Agent: *agent}, Out: os.Stdout}
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

type managedLabel struct {
	name, color, description string
}

var managedLabels = []managedLabel{
	{name: "agent-ready", color: "0E8A16", description: "Issue is ready for an agent"},
	{name: "agent-started", color: "FBCA04", description: "An agent is working on this issue"},
}

func (g GHCLI) EnsureLabels(ctx context.Context, repo string) error {
	for _, label := range managedLabels {
		cmd := exec.CommandContext(ctx, g.Binary, "label", "create", label.name, "--repo", repo, "--color", label.color, "--description", label.description, "--force")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ensure %s label: %w: %s", label.name, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func (g GHCLI) ListIssues(ctx context.Context, repo string) ([]Issue, error) {
	args := issueListArgs(repo, g.Filter, g.AllIssues)
	cmd := exec.CommandContext(ctx, g.Binary, args...)
	return decodeIssues(cmd.Output())
}

func issueListArgs(repo, filter string, allIssues bool) []string {
	args := []string{"issue", "list", "--repo", repo, "--state", "open", "--limit", "1000"}
	if !allIssues && filter != "" {
		args = append(args, "--search", filter)
	}
	return append(args, "--json", "number,title,state,createdAt,labels")
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

type CommandRunner struct{ Binary, Agent string }

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
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
func validRepo(repo string) bool {
	parts := strings.Split(repo, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(repo, " \t\r\n")
}
