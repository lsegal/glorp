package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
)

var version = "dev"

var errProjectIssueNotFound = errors.New("project issue not found")

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	interval := flag.Duration("interval", 30*time.Second, "time between GitHub issue polls")
	poll := flag.Bool("poll", false, "poll GitHub instead of waiting for webhooks")
	listen := flag.String("listen", ":0", "address for the GitHub webhook server")
	webhookPath := flag.String("webhook-path", "/webhook", "path for GitHub webhook deliveries")
	webhookSecret := flag.String("webhook-secret", "", "optional GitHub webhook secret")
	ngrokBinary := flag.String("ngrok-binary", "ngrok", "ngrok executable")
	ngrokAPI := flag.String("ngrok-api", "http://127.0.0.1:4040", "ngrok local API URL")
	noUI := flag.Bool("no-ui", false, "disable the interactive terminal UI")
	yolo := flag.Bool("yolo", false, "disable agent sandboxes and permission checks")
	concurrency := flag.Int("concurrency", 0, "maximum concurrent agents (0 means 3)")
	agent := flag.String("agent", "codex", "agent to run: codex or claude")
	model := flag.String("model", "", "model to use for the agent")
	modelLevel := flag.String("model-level", "", "model reasoning level: low, medium, or high")
	readyState := flag.String("ready-state", "", "project status that marks an issue ready for an agent")
	codexBinary := flag.String("codex-binary", "codex", "Codex executable")
	claudeBinary := flag.String("claude-binary", "claude", "Claude executable")
	statePath := flag.String("state", ".glorp.json", "file used to remember handled issue numbers")
	filter := filterFlag{values: []string{defaultIssueFilter}}
	flag.Var(&filter, "filter", "GitHub issue search filter (repeatable)")
	allIssues := flag.Bool("all-issues", false, "disable the default issue filter")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: glorp [flags] TARGET [TARGET ...]")
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
	if *modelLevel != "" && *modelLevel != "low" && *modelLevel != "medium" && *modelLevel != "high" {
		fmt.Fprintln(os.Stderr, "model-level must be low, medium, or high")
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
	gh := GHCLI{Binary: "gh", ReadyState: strings.TrimSpace(*readyState)}
	gh.Filter, gh.AllIssues = filter.String(), *allIssues
	targets := flag.Args()
	events := make(chan WebhookEvent, 1)
	output := io.Writer(os.Stdout)
	var ui *TerminalUI
	if shouldUseUI(*noUI, os.Stdout) {
		ui = NewTerminalUI()
		output = ui.Writer()
		go func() { _ = ui.Run(ctx) }()
	}
	wOut := output
	if ui != nil {
		wOut = io.Discard
	}
	var quota func(context.Context) string
	if *agent == "codex" {
		quotaReader := &codexQuotaReader{Binary: binary}
		quota = quotaReader.Read
	}
	w := &Glorp{Repo: targets[0], Targets: targets, Interval: *interval, UseWebhooks: !*poll, Events: events, Concurrency: limit, StatePath: *statePath, ReadyState: gh.ReadyState, Issues: gh, Labels: gh, Status: gh, UI: terminalUIReporter(ui), Quota: quota, Runner: CommandRunner{Binary: binary, Agent: *agent, Model: *model, ModelLevel: *modelLevel, Repo: targets[0], Yolo: *yolo}, Out: wOut}
	var server *http.Server
	if !*poll {
		listener, err := listenForWebhooks(*listen)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		server = &http.Server{Addr: *listen, Handler: WebhookHandler{Events: events, Secret: *webhookSecret, WebhookPath: *webhookPath}}
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "webhook server: %v\n", err)
			}
		}()
		defer server.Close()
		listenAddr := listener.Addr().String()
		fmt.Fprintf(output, "webhook server listening on %s%s\n", listenAddr, *webhookPath)
		fmt.Fprintf(output, "starting ngrok tunnel for %s\n", listenAddr)
		tunnel, err := startNgrok(ctx, *ngrokBinary, listenAddr, *ngrokAPI, output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer tunnel.Close()
		endpoint, err := webhookURL(tunnel.URL(), *webhookPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(output, "ngrok tunnel ready at %s\n", tunnel.URL())
		configured := 0
		for _, target := range targets {
			if err := gh.ConfigureWebhook(ctx, target, endpoint, *webhookSecret); err != nil {
				if errors.Is(err, errProjectWebhookUnavailable) {
					fmt.Fprintln(output, err)
					continue
				}
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			configured++
			fmt.Fprintf(output, "configured GitHub webhook for %s\n", target)
		}
		if configured == 0 {
			fmt.Fprintln(output, "no targets available for webhook configuration")
		}
	}
	if err := w.Run(ctx); err != nil {
		if ui != nil {
			ui.program.Quit()
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if ui != nil {
		ui.program.Quit()
	}
}

func listenForWebhooks(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for webhooks on %s: %w", address, err)
	}
	return listener, nil
}

func isTerminal(file *os.File) bool {
	fd := file.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func shouldUseUI(disabled bool, output *os.File) bool {
	return !disabled && isTerminal(output)
}

func terminalUIReporter(ui *TerminalUI) UIReporter {
	if ui == nil {
		return nil
	}
	return ui
}

type GHCLI struct {
	Binary     string
	Filter     string
	AllIssues  bool
	ReadyState string
}

const defaultIssueFilter = "is:issue state:open author:@me"

type filterFlag struct {
	values []string
	set    bool
}

func (f *filterFlag) String() string {
	return strings.Join(f.values, " ")
}

func (f *filterFlag) Set(value string) error {
	if !f.set {
		f.values = nil
		f.set = true
	}
	f.values = append(f.values, value)
	return nil
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
	var issues []Issue
	if target.isProject {
		issues, err = decodeProjectIssues(output, err)
	} else {
		issues, err = decodeIssues(output, err)
	}
	if err != nil {
		return nil, err
	}
	for i := range issues {
		if err := g.loadDependencies(ctx, target.repo, &issues[i]); err != nil {
			return nil, err
		}
	}
	return issues, nil
}

func issueListArgs(repo, filter string, allIssues bool) []string {
	target, err := parseTarget(repo)
	if err != nil || target.isProject {
		return nil
	}
	args := []string{"issue", "list", "--repo", target.repo, "--state", "open", "--limit", "1000"}
	if !allIssues && filter != "" {
		args = append(args, "--search", filter)
	}
	return append(args, "--json", "number,title,body,state,createdAt,labels")
}

type target struct {
	repo, owner, projectID string
	projectOwnerType       string
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
		return target{owner: parts[1], projectID: parts[3], projectOwnerType: parts[0], isProject: true}, nil
	}
	if len(parts) == 4 && parts[2] == "projects" && parts[0] != "" && parts[1] != "" && parts[3] != "" {
		return target{repo: parts[0] + "/" + parts[1], owner: parts[0], projectID: parts[3], isProject: true}, nil
	}
	return target{}, fmt.Errorf("target must be OWNER/REPO or a GitHub repository/project URL")
}

func projectListArgs(t target, filter string, allIssues bool) []string {
	args := []string{"project", "item-list", t.projectID, "--owner", t.owner, "--format", "json", "--limit", "1000"}
	query := "is:issue is:open"
	if !allIssues && filter != "" && filter != defaultIssueFilter {
		query += " " + filter
	}
	return append(args, "--query", query)
}

func decodeIssues(data []byte, err error) ([]Issue, error) {
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	return parseIssues(data)
}

var dependencyPattern = regexp.MustCompile(`(?i)\bdepends\s+on\s+#(\d+)`)

func (g GHCLI) loadDependencies(ctx context.Context, repo string, issue *Issue) error {
	dependencies := make(map[int]IssueDependency)
	for _, match := range dependencyPattern.FindAllStringSubmatch(issue.Body, -1) {
		number := 0
		if _, err := fmt.Sscanf(match[1], "%d", &number); err == nil && number > 0 {
			dependency := IssueDependency{Number: number}
			if repo != "" {
				cmd := exec.CommandContext(ctx, g.Binary, "issue", "view", fmt.Sprint(number), "--repo", repo, "--json", "state")
				output, viewErr := cmd.Output()
				if viewErr != nil {
					return fmt.Errorf("read dependency #%d for issue #%d: %w", number, issue.Number, viewErr)
				}
				var state struct {
					State string `json:"state"`
				}
				if err := json.Unmarshal(output, &state); err != nil {
					return fmt.Errorf("decode dependency #%d for issue #%d: %w", number, issue.Number, err)
				}
				dependency.State = state.State
			}
			dependencies[number] = dependency
		}
	}
	if repo != "" {
		cmd := exec.CommandContext(ctx, g.Binary, "api", "repos/"+repo+"/issues/"+fmt.Sprint(issue.Number)+"/dependencies/blocked_by", "--header", "X-GitHub-Api-Version: 2022-11-28")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("list dependencies for issue #%d: %w: %s", issue.Number, err, strings.TrimSpace(string(output)))
		}
		var related []IssueDependency
		if err := json.Unmarshal(output, &related); err != nil {
			return fmt.Errorf("decode dependencies for issue #%d: %w", issue.Number, err)
		}
		for _, dependency := range related {
			dependencies[dependency.Number] = dependency
		}
	}
	issue.DependsOn = issue.DependsOn[:0]
	for _, dependency := range dependencies {
		issue.DependsOn = append(issue.DependsOn, dependency)
	}
	slices.SortFunc(issue.DependsOn, func(a, b IssueDependency) int { return a.Number - b.Number })
	return nil
}

func (g GHCLI) SetIssueLabel(ctx context.Context, repo string, number int, add bool) error {
	target, err := parseTarget(repo)
	if err != nil {
		return err
	}
	if target.isProject {
		return nil
	}
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

func (g GHCLI) SetIssueStatus(ctx context.Context, repo string, issue Issue, status string) error {
	target, err := parseTarget(repo)
	if err != nil {
		return err
	}
	if !target.isProject {
		return nil
	}

	itemID := issue.ProjectItemID
	if itemID == "" {
		list := exec.CommandContext(ctx, g.Binary, projectListArgs(target, "", true)...)
		output, err := list.Output()
		items, err := decodeProjectItems(output, err)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Content != nil && item.Content.Number == issue.Number {
				itemID = item.ID
				break
			}
		}
	}
	if itemID == "" {
		return fmt.Errorf("%w: issue #%d is not in project %s", errProjectIssueNotFound, issue.Number, target.projectID)
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
	fieldID, optionID := projectStatusOption(fields, status, g.ReadyState == "")
	if fieldID == "" || optionID == "" {
		return fmt.Errorf("project %s has no Status option %q", target.projectID, status)
	}

	edit := exec.CommandContext(ctx, g.Binary, "project", "item-edit", "--id", itemID, "--field-id", fieldID, "--project-id", view.ID, "--single-select-option-id", optionID)
	if output, err := edit.CombinedOutput(); err != nil {
		return projectStatusError(issue.Number, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func projectStatusOption(fields []projectField, status string, allowReadyFallback bool) (string, string) {
	for _, field := range fields {
		if field.Name != "Status" {
			continue
		}
		statuses := []string{status}
		if allowReadyFallback && strings.EqualFold(strings.TrimSpace(status), "Todo") {
			statuses = append(statuses, "Ready")
		}
		for _, candidate := range statuses {
			for _, option := range field.Options {
				if strings.EqualFold(strings.TrimSpace(option.Name), strings.TrimSpace(candidate)) {
					return field.ID, option.ID
				}
			}
		}
		return field.ID, ""
	}
	return "", ""
}

func projectStatusError(number int, err error, detail string) error {
	if strings.Contains(detail, "missing required scopes") && strings.Contains(detail, "[project]") {
		return fmt.Errorf("update project status for issue #%d: %w: %s; authenticate with the project scope using `gh auth refresh -s project`", number, err, detail)
	}
	return fmt.Errorf("update project status for issue #%d: %w: %s", number, err, detail)
}

type CommandRunner struct {
	Binary, Agent, Model, ModelLevel, Repo string
	Output                                 io.Writer
	Yolo                                   bool
}

type codexJSONOutputWriter struct {
	output io.Writer
	buffer []byte
}

type codexJSONEvent struct {
	Type string `json:"type"`
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
	} `json:"item"`
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
}

func newCodexJSONOutputWriter(output io.Writer) *codexJSONOutputWriter {
	return &codexJSONOutputWriter{output: output}
}

func (w *codexJSONOutputWriter) Write(p []byte) (int, error) {
	w.buffer = append(w.buffer, p...)
	for {
		newline := bytes.IndexByte(w.buffer, '\n')
		if newline < 0 {
			break
		}
		line := append([]byte(nil), w.buffer[:newline]...)
		w.buffer = w.buffer[newline+1:]
		if err := w.writeLine(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *codexJSONOutputWriter) Flush() error {
	if len(w.buffer) == 0 {
		return nil
	}
	line := append([]byte(nil), w.buffer...)
	w.buffer = nil
	return w.writeLine(line)
}

func (w *codexJSONOutputWriter) writeLine(line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var event codexJSONEvent
	if err := json.Unmarshal(line, &event); err != nil {
		_, err = fmt.Fprintln(w.output, string(line))
		return err
	}
	var text string
	switch {
	case event.Type == "item.completed" && (event.Item.Type == "agent_message" || event.Item.Type == "reasoning"):
		text = event.Item.Text
	case event.Type == "item.started" && event.Item.Type == "command_execution" && event.Item.Command != "":
		text = "Running: " + event.Item.Command
	case event.Type == "turn.failed" && event.Error.Message != "":
		text = "Agent error: " + event.Error.Message
	case event.Type == "error" && event.Message != "":
		text = "Agent error: " + event.Message
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	_, err := fmt.Fprintln(w.output, text)
	return err
}

func commandArgs(r CommandRunner, issue Issue) []string {
	target := issue.Target
	if target == "" {
		target = r.Repo
	}
	prompt := fmt.Sprintf("/gh-fix %d", issue.Number)
	if repo := issueRepository(target, issue); repo != "" {
		prompt += "\n\nRepository: " + repo
	}
	prompt += "\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	if r.Agent == "codex" {
		// JSONL keeps Codex's progress events flowing when stdout is a pipe,
		// which is how the dashboard captures agent output.
		args := []string{"exec"}
		if r.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		args = append(args, "--json")
		if r.Model != "" {
			args = append(args, "--model", r.Model)
		}
		if r.ModelLevel != "" {
			args = append(args, "-c", "model_reasoning_effort="+r.ModelLevel)
		}
		return append(args, prompt)
	}
	args := []string{"-p"}
	if r.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.ModelLevel != "" {
		args = append(args, "--effort", r.ModelLevel)
	}
	return append(args, prompt)
}

func (r CommandRunner) Run(ctx context.Context, issue Issue) error {
	return r.run(ctx, issue, nil)
}

func (r CommandRunner) RunWithOutput(ctx context.Context, issue Issue, output io.Writer) error {
	return r.run(ctx, issue, output)
}

func newAgentCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	// Codex treats non-terminal stdin as an optional prompt stream even when
	// the positional prompt is already provided. Preserve a terminal stdin for
	// interactive launches so it does not print the additional-input notice;
	// use the null device when running headlessly so the agent cannot consume
	// unrelated input or wait on an open pipe.
	if isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		cmd.Stdin = os.Stdin
	}
	return cmd
}

func (r CommandRunner) run(ctx context.Context, issue Issue, jobOutput io.Writer) error {
	args := commandArgs(r, issue)
	cmd := newAgentCommand(ctx, r.Binary, args...)
	stdout := io.Writer(io.Discard)
	stderr := io.Writer(io.Discard)
	var codexOutput *codexJSONOutputWriter
	if jobOutput != nil {
		jobStdout := jobOutput
		if r.Agent == "codex" {
			codexOutput = newCodexJSONOutputWriter(jobOutput)
			jobStdout = codexOutput
		}
		stdout = io.MultiWriter(stdout, jobStdout)
		stderr = io.MultiWriter(stderr, jobOutput)
	}
	if r.Output != nil {
		stdout = io.MultiWriter(stdout, r.Output)
		stderr = io.MultiWriter(stderr, r.Output)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	if codexOutput != nil {
		if err := codexOutput.Flush(); runErr == nil && err != nil {
			runErr = err
		}
	}
	if runErr != nil {
		repo := r.Repo
		if issue.Target != "" {
			repo = issueRepository(issue.Target, issue)
		}
		report, reportErr := bugReportURL(repo, issue, args)
		if reportErr != nil {
			return fmt.Errorf("agent failed: %w (could not create bug report URL: %v)", runErr, reportErr)
		}
		return fmt.Errorf("agent failed: %w; bug report: %s", runErr, report)
	}
	return nil
}

func bugReportURL(repo string, issue Issue, args []string) (string, error) {
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
	values.Set("body", fmt.Sprintf("## Context\n\n- Repository: `%s`\n- Issue: #%d\n- Command: `%s`\n\n## Agent output\n\n[robot output omitted]\n", target.repo, issue.Number, strings.Join(args, " ")))
	return "https://github.com/" + target.repo + "/issues/new?" + values.Encode(), nil
}
func validRepo(repo string) bool {
	parts := strings.Split(repo, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(repo, " \t\r\n")
}
