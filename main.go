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
	w := &Watcher{Repo: flag.Arg(0), Interval: *interval, Concurrency: limit, StatePath: *statePath, Issues: GHCLI{Binary: "gh", Filter: *filter, AllIssues: *allIssues}, Runner: CommandRunner{Binary: binary, Agent: *agent}, Out: os.Stdout}
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
	return append(args, "--json", "number,title,state,createdAt")
}
func decodeIssues(data []byte, err error) ([]Issue, error) {
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	return parseIssues(data)
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
