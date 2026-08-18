package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
)

var version = "dev"

var errProjectIssueNotFound = errors.New("project issue not found")

// watchFlagSet builds the `glorp watch` flag set. It is also used, without
// being parsed, to print the command's defaults in `glorp help watch`.
func watchFlagSet(agents *agentFlag, filter *filterFlag) *flag.FlagSet {
	flags := flag.NewFlagSet("watch", flag.ExitOnError)
	flags.Duration("interval", 30*time.Second, "time between GitHub issue polls")
	flags.Bool("poll", false, "poll GitHub instead of waiting for webhooks")
	flags.String("listen", ":0", "address for the GitHub webhook server")
	flags.String("webhook-path", "/webhook", "path for GitHub webhook deliveries")
	flags.String("webhook-secret", "", "optional GitHub webhook secret")
	flags.String("ngrok-binary", "ngrok", "ngrok executable")
	flags.String("ngrok-api", "http://127.0.0.1:4040", "ngrok local API URL")
	flags.String("ui", "web", "user interface: web, tui, or none")
	flags.Bool("no-ui", false, "disable all UI (equivalent to --ui none)")
	flags.Int("web-ui-port", defaultWebUIPort, "starting port for the browser UI")
	flags.Bool("yolo", false, "disable agent sandboxes and permission checks")
	flags.Int("concurrency", 0, "maximum concurrent agents (0 means 3)")
	flags.Var(agents, "agent", "agent to run as agent/model:level, such as codex, claude/opus, or codex/gpt-5.6:high (repeatable to load balance evenly across concurrency)")
	flags.String("ready-state", "", "project status that marks an issue ready for an agent")
	flags.String("codex-binary", "codex", "Codex executable")
	flags.String("claude-binary", "claude", "Claude executable")
	flags.String("state", ".glorp.json", "file used to remember handled issue numbers")
	flags.Var(filter, "filter", "GitHub issue search filter (repeatable)")
	flags.Bool("all-issues", false, "disable the default issue filter")
	return flags
}

// commandFlags returns the flag set backing a subcommand so help can print its
// defaults, or nil for commands that take no flags.
func commandFlags(name string) *flag.FlagSet {
	switch name {
	case "watch":
		return watchFlagSet(&agentFlag{values: []agentSpec{{Name: "codex"}}}, &filterFlag{values: []string{defaultIssueFilter}})
	case "ui":
		return uiFlagSet()
	}
	return nil
}

func runWatch(args []string) int {
	agents := agentFlag{values: []agentSpec{{Name: "codex"}}}
	filter := filterFlag{values: []string{defaultIssueFilter}}
	flags := watchFlagSet(&agents, &filter)
	flags.Usage = func() {
		cmd, _ := lookupCommand("watch")
		fmt.Fprintln(os.Stderr, cmd.usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	interval := flagValue[time.Duration](flags, "interval")
	poll := flagValue[bool](flags, "poll")
	listen := flagValue[string](flags, "listen")
	webhookPath := flagValue[string](flags, "webhook-path")
	webhookSecret := flagValue[string](flags, "webhook-secret")
	ngrokBinary := flagValue[string](flags, "ngrok-binary")
	ngrokAPI := flagValue[string](flags, "ngrok-api")
	uiMode := flagValue[string](flags, "ui")
	noUI := flagValue[bool](flags, "no-ui")
	webUIPort := flagValue[int](flags, "web-ui-port")
	yolo := flagValue[bool](flags, "yolo")
	concurrency := flagValue[int](flags, "concurrency")
	readyState := flagValue[string](flags, "ready-state")
	codexBinary := flagValue[string](flags, "codex-binary")
	claudeBinary := flagValue[string](flags, "claude-binary")
	statePath := flagValue[string](flags, "state")
	allIssues := flagValue[bool](flags, "all-issues")
	targets := flags.Args()
	if len(targets) == 0 {
		if repo, ok := originRemoteTarget(); ok {
			targets = []string{repo}
		}
	}
	if len(targets) == 0 {
		flags.Usage()
		return 2
	}
	for i, value := range targets {
		expanded, err := expandTargetShorthand(value, originRemoteTarget)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		targets[i] = expanded
	}
	if interval <= 0 || concurrency < 0 {
		fmt.Fprintln(os.Stderr, "interval must be positive and concurrency cannot be negative")
		return 2
	}
	mode, err := selectedUIMode(uiMode, noUI)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if mode == "web" && (webUIPort < 1 || webUIPort > 65535) {
		fmt.Fprintln(os.Stderr, "web-ui-port must be between 1 and 65535")
		return 2
	}
	limit := concurrency
	if limit == 0 {
		limit = 3
	}
	binary := codexBinary
	if agents.values[0].Name == "claude" {
		binary = claudeBinary
	}
	ctx, stop := shutdownContext()
	defer stop()
	// Nothing glorp started may outlive it, so sweep up any subprocess whose own
	// cleanup did not run before the daemon returns (issue #260).
	defer reapChildProcesses()
	gh := GHCLI{Binary: "gh", ReadyState: strings.TrimSpace(readyState), publicRepoCache: &sync.Map{}, selfLoginCache: &sync.Map{}}
	gh.Filter, gh.AllIssues = filter.String(), allIssues
	events := make(chan WebhookEvent, 1)
	output := io.Writer(os.Stdout)
	var ui *TerminalUI
	if shouldUseTerminalUI(mode, os.Stdout) {
		ui = NewTerminalUI()
		output = ui.Writer()
		go func() { _ = ui.Run(ctx) }()
	}
	var webUI *WebUI
	var webServer *http.Server
	if mode == "web" {
		var err error
		webUI, err = NewWebUI()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		listener, port, err := listenForWebUI(webUIPort)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// Record the bound port so `glorp ui` finds this instance even when it
		// listens far away from the default scan range.
		removeRecord, err := writeDashboardRecord(dashboardRecordsDir(os.Getenv), port, os.Getpid())
		if err != nil {
			fmt.Fprintf(os.Stderr, "record dashboard port: %v\n", err)
		}
		defer removeRecord()
		webServer = &http.Server{Handler: webUI}
		stopFrontend, err := startWebUIFrontend(ctx, output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start web UI frontend: %v\n", err)
			return 1
		}
		defer stopFrontend()
		go func() {
			if err := webServer.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "web UI server: %v\n", err)
			}
		}()
		defer webServer.Close()
		webURL := fmt.Sprintf("http://localhost:%d", port)
		fmt.Fprintf(output, "web UI listening on %s\n", webURL)
		webUI.Log("web UI listening on " + webURL)
	} else if mode == "none" {
		fmt.Fprintln(output, "UI disabled")
	}
	wOut := output
	if ui != nil {
		wOut = io.Discard
	}
	quota := combinedQuotaReader(namedQuotaReaders(agents.names(), func(agent string) string {
		if agent == "claude" {
			return claudeBinary
		}
		return codexBinary
	}))
	var agentCursor *atomic.Uint64
	if len(agents.values) > 1 {
		agentCursor = &atomic.Uint64{}
	}
	identity, err := newIdentity()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	w := &Glorp{Repo: targets[0], Targets: targets, Interval: interval, UseWebhooks: !poll, Events: events, Concurrency: limit, StatePath: statePath, ReadyState: gh.ReadyState, Issues: gh, Discussions: gh, Labels: gh, Status: gh, Comments: gh, Projects: gh, Identity: identity, UI: combineUIReporters(terminalUIReporter(ui), webUI), Quota: quota, Runner: CommandRunner{Binary: binary, CodexBinary: codexBinary, ClaudeBinary: claudeBinary, Agents: agents.specs(), Agent: agents.values[0].String(), Repo: targets[0], Yolo: yolo, agentCursor: agentCursor}, Out: wOut}
	var server *http.Server
	if !poll {
		listener, err := listenForWebhooks(listen)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		server = &http.Server{Addr: listen, Handler: WebhookHandler{Events: events, Secret: webhookSecret, WebhookPath: webhookPath}}
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "webhook server: %v\n", err)
			}
		}()
		defer server.Close()
		listenAddr := listener.Addr().String()
		fmt.Fprintf(output, "webhook server listening on %s%s\n", listenAddr, webhookPath)
		fmt.Fprintf(output, "starting ngrok tunnel for %s\n", listenAddr)
		tunnel, err := startNgrok(ctx, ngrokBinary, listenAddr, ngrokAPI, output)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer tunnel.Close()
		endpoint, err := webhookURL(tunnel.URL(), webhookPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintf(output, "ngrok tunnel ready at %s\n", tunnel.URL())
		configured := 0
		for _, target := range targets {
			if _, err := gh.ConfigureWebhook(ctx, target, endpoint, webhookSecret); err != nil {
				if errors.Is(err, errProjectWebhookUnavailable) {
					fmt.Fprintln(output, err)
					continue
				}
				if !errors.Is(err, errWebhookPartiallyConfigured) {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
				fmt.Fprintln(output, err)
			}
			configured++
			fmt.Fprintf(output, "configured GitHub webhook for %s\n", target)
		}
		if configured == 0 {
			fmt.Fprintln(output, "no targets available for webhook configuration")
		}
		// A project board can gain a repository that was not on it at startup,
		// and that repository has no webhook until the target is configured
		// again, so keep reconciling while the daemon runs (issue #238).
		w.Webhooks = newWebhookReconciler(gh, targets, endpoint, webhookSecret, w.logf).reconcile
	}
	if err := w.Run(ctx); err != nil {
		if ui != nil {
			ui.program.Quit()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if ui != nil {
		ui.program.Quit()
	}
	return 0
}

// flagValue reads a parsed flag's value from a flag set by name, keeping the
// watch command's long option list readable without threading two dozen
// pointers through it.
func flagValue[T any](flags *flag.FlagSet, name string) T {
	var zero T
	found := flags.Lookup(name)
	if found == nil {
		return zero
	}
	getter, ok := found.Value.(flag.Getter)
	if !ok {
		return zero
	}
	value, ok := getter.Get().(T)
	if !ok {
		return zero
	}
	return value
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

func selectedUIMode(mode string, noUI bool) (string, error) {
	if noUI {
		return "none", nil
	}
	switch mode {
	case "web", "tui", "none":
		return mode, nil
	default:
		return "", fmt.Errorf("ui must be web, tui, or none")
	}
}

func shouldUseTerminalUI(mode string, output *os.File) bool {
	return mode == "tui" && shouldUseUI(false, output)
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
	runCommand func(context.Context, ...string) ([]byte, error)
	// publicAPI overrides the unauthenticated public GitHub API request used
	// to poll public repositories without spending the authenticated gh
	// token's rate limit. Nil uses the real network implementation.
	publicAPI publicAPIDoer
	// publicRepoCache memoizes repo visibility across the lifetime of a run.
	// It is a pointer so copies of GHCLI (it is passed by value throughout)
	// share one cache; nil (the zero value used in tests that build GHCLI
	// literals directly) simply disables memoization rather than panicking.
	publicRepoCache *sync.Map
	// selfLoginCache memoizes the authenticated gh user's login, needed only
	// to translate the "author:@me" search qualifier for the public API.
	selfLoginCache *sync.Map
}

func (g GHCLI) run(ctx context.Context, args ...string) ([]byte, error) {
	if g.runCommand != nil {
		return g.runCommand(ctx, args...)
	}
	return combinedOutputChildProcess(exec.CommandContext(ctx, g.Binary, args...))
}

func (g GHCLI) OriginatingWorkState(ctx context.Context, repo string, number int) (OriginatingWorkState, error) {
	public := g.isPublicRepo(ctx, repo)
	output, err := g.apiGET(ctx, public, "repos/"+repo+"/issues/"+strconv.Itoa(number))
	if err != nil {
		return OriginatingWorkState{}, fmt.Errorf("read issue #%d state: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	var issue struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &issue); err != nil {
		return OriginatingWorkState{}, fmt.Errorf("decode issue #%d state: %w", number, err)
	}
	output, err = g.apiGET(ctx, public, "repos/"+repo+"/issues/"+strconv.Itoa(number)+"/timeline")
	if err != nil {
		return OriginatingWorkState{}, fmt.Errorf("read issue #%d timeline: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	var events []struct {
		Event  string `json:"event"`
		Source struct {
			Issue struct {
				Number      int    `json:"number"`
				State       string `json:"state"`
				Body        string `json:"body"`
				PullRequest *struct {
					MergedAt *time.Time `json:"merged_at"`
				} `json:"pull_request"`
			} `json:"issue"`
		} `json:"source"`
	}
	if err := json.Unmarshal(output, &events); err != nil {
		return OriginatingWorkState{}, fmt.Errorf("decode issue #%d timeline: %w", number, err)
	}
	state := OriginatingWorkState{IssueState: issue.State}
	for _, event := range events {
		pullRequest := event.Source.Issue
		if event.Event != "cross-referenced" || pullRequest.PullRequest == nil || !closesIssue(pullRequest.Body, repo, number) {
			continue
		}
		output, err := g.apiGET(ctx, public, "repos/"+repo+"/pulls/"+strconv.Itoa(pullRequest.Number))
		if err != nil {
			return OriginatingWorkState{}, fmt.Errorf("read pull request #%d state for issue #%d: %w: %s", pullRequest.Number, number, err, strings.TrimSpace(string(output)))
		}
		var currentPullRequest struct {
			State    string     `json:"state"`
			MergedAt *time.Time `json:"merged_at"`
		}
		if err := json.Unmarshal(output, &currentPullRequest); err != nil {
			return OriginatingWorkState{}, fmt.Errorf("decode pull request #%d state for issue #%d: %w", pullRequest.Number, number, err)
		}
		state.PullRequests = append(state.PullRequests, PullRequestWorkState{Number: pullRequest.Number, State: currentPullRequest.State, Merged: currentPullRequest.MergedAt != nil})
	}
	return state, nil
}

func closesIssue(body, repo string, number int) bool {
	pattern := `(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+(?:` + regexp.QuoteMeta(repo) + `)?#` + strconv.Itoa(number) + `\b`
	return regexp.MustCompile(pattern).MatchString(body)
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

// agentSpec is a single --agent value: the agent to run plus the optional
// model and reasoning level to run it with. Each agent carries its own model
// so repeated --agent values can mix agents and models freely.
type agentSpec struct {
	Name  string
	Model string
	Level string
}

// String renders the spec back into its provider/model:level form so it can be
// persisted in the work state and parsed again when a session resumes.
func (s agentSpec) String() string {
	value := s.Name
	if s.Model != "" {
		value += "/" + s.Model
	}
	if s.Level != "" {
		value += ":" + s.Level
	}
	return value
}

// parseAgentSpec parses provider/model:level, where the model and level are
// both optional (for example codex, claude/opus, or codex/gpt-5.6:high).
func parseAgentSpec(value string) (agentSpec, error) {
	name := strings.TrimSpace(value)
	var spec agentSpec
	if index := strings.LastIndex(name, ":"); index >= 0 {
		spec.Level = strings.TrimSpace(name[index+1:])
		name = strings.TrimSpace(name[:index])
		if spec.Level != "low" && spec.Level != "medium" && spec.Level != "high" {
			return agentSpec{}, fmt.Errorf("agent level must be low, medium, or high")
		}
	}
	if base, model, ok := strings.Cut(name, "/"); ok {
		name, spec.Model = strings.TrimSpace(base), strings.TrimSpace(model)
		if spec.Model == "" {
			return agentSpec{}, fmt.Errorf("agent model cannot be empty")
		}
	}
	if name != "codex" && name != "claude" {
		return agentSpec{}, fmt.Errorf("agent must be codex or claude")
	}
	spec.Name = name
	return spec, nil
}

// agentProvider returns the agent name from a spec, falling back to the raw
// value so work state written by older versions still resolves.
func agentProvider(value string) string {
	spec, err := parseAgentSpec(value)
	if err != nil {
		return value
	}
	return spec.Name
}

// agentFlag collects repeated --agent values so multiple agents can be
// configured and load balanced evenly across concurrency.
type agentFlag struct {
	values []agentSpec
	set    bool
}

func (f *agentFlag) String() string {
	names := make([]string, 0, len(f.values))
	for _, spec := range f.values {
		names = append(names, spec.String())
	}
	return strings.Join(names, ",")
}

func (f *agentFlag) Set(value string) error {
	spec, err := parseAgentSpec(value)
	if err != nil {
		return err
	}
	if !f.set {
		f.values = nil
		f.set = true
	}
	f.values = append(f.values, spec)
	return nil
}

// specs renders every configured agent into its string form for the runner.
func (f *agentFlag) specs() []string {
	specs := make([]string, 0, len(f.values))
	for _, spec := range f.values {
		specs = append(specs, spec.String())
	}
	return specs
}

// names lists the agent names, without models, for quota lookups.
func (f *agentFlag) names() []string {
	names := make([]string, 0, len(f.values))
	for _, spec := range f.values {
		names = append(names, spec.Name)
	}
	return names
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

type projectItemsPage struct {
	Data struct {
		User         projectItemsOwner `json:"user"`
		Organization projectItemsOwner `json:"organization"`
	} `json:"data"`
}

type projectItemsOwner struct {
	Project projectItemsProject `json:"projectV2"`
}

type projectItemsProject struct {
	Items struct {
		Nodes    []projectGraphQLItem `json:"nodes"`
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"items"`
}

type projectGraphQLItem struct {
	ID               string `json:"id"`
	FieldValueByName struct {
		Name string `json:"name"`
	} `json:"fieldValueByName"`
	Content struct {
		Type       string    `json:"__typename"`
		Number     int       `json:"number"`
		Title      string    `json:"title"`
		Body       string    `json:"body"`
		State      string    `json:"state"`
		CreatedAt  time.Time `json:"createdAt"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		Labels struct {
			Nodes []IssueLabel `json:"nodes"`
		} `json:"labels"`
	} `json:"content"`
}

type repositoryProjectItem struct {
	ID      string `json:"id"`
	Project struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Owner  struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"project"`
}

type repositoryProjectItemsPage struct {
	Data struct {
		Repository struct {
			Issue struct {
				ProjectItems struct {
					Nodes    []repositoryProjectItem `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"projectItems"`
			} `json:"issue"`
		} `json:"repository"`
	} `json:"data"`
}

type managedLabel struct {
	name, color, description string
}

var managedLabels = []managedLabel{
	{name: "agent-ready", color: "0E8A16", description: "Issue is ready for an agent"},
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
		if output, err := combinedOutputChildProcess(cmd); err != nil {
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
	if target.isProject && target.projectOwnerType != "" {
		items, err := g.listProjectItems(ctx, target, g.Filter, g.AllIssues)
		if err != nil {
			return nil, err
		}
		issues := issuesFromProjectItems(items)
		for i := range issues {
			if err := g.loadDependencies(ctx, target.repo, &issues[i]); err != nil {
				return nil, err
			}
		}
		return issues, nil
	}
	if target.isProject {
		// A repository-scoped project board (owner/repo/projects/N) leaves
		// projectOwnerType unset; it still needs the GraphQL-only Projects
		// v2 API, which has no REST equivalent.
		issues, err := decodeProjectIssues(g.run(ctx, projectListArgs(target, g.Filter, g.AllIssues)...))
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

	var issues []Issue
	usedPublicAPI := false
	if g.isPublicRepo(ctx, target.repo) {
		if got, ok := g.listPublicIssues(ctx, target.repo, g.Filter, g.AllIssues); ok {
			issues, usedPublicAPI = got, true
		}
	}
	if !usedPublicAPI {
		got, err := g.listAuthenticatedIssues(ctx, target.repo, g.Filter, g.AllIssues)
		if err != nil {
			return nil, err
		}
		issues = got
	}
	for i := range issues {
		if err := g.loadDependencies(ctx, target.repo, &issues[i]); err != nil {
			return nil, err
		}
	}
	return issues, nil
}

type discussionsGraphQLResponse struct {
	Data struct {
		Repository struct {
			Discussions struct {
				Nodes []struct {
					Number    int       `json:"number"`
					Title     string    `json:"title"`
					Body      string    `json:"body"`
					CreatedAt time.Time `json:"createdAt"`
					Comments  struct {
						TotalCount int `json:"totalCount"`
					} `json:"comments"`
					Category struct {
						Slug string `json:"slug"`
						Name string `json:"name"`
					} `json:"category"`
				} `json:"nodes"`
			} `json:"discussions"`
		} `json:"repository"`
	} `json:"data"`
}

const discussionsQuery = `query($owner:String!,$name:String!){
  repository(owner:$owner,name:$name){
    discussions(first:100,orderBy:{field:CREATED_AT,direction:DESC}){
      nodes{number title body createdAt comments(first:1){totalCount} category{slug name}}
    }
  }
}`

// ListUnansweredDiscussions lists top-level Discussion threads that have not
// received any reply yet. A discussion's own reply count doubles as the
// record of whether it still needs an answer, so no local state is needed
// to avoid re-dispatching an already-answered thread.
func (g GHCLI) ListUnansweredDiscussions(ctx context.Context, repo string) ([]Discussion, error) {
	target, err := parseTarget(repo)
	if err != nil {
		return nil, err
	}
	if !target.isDiscussion {
		return nil, fmt.Errorf("target %q is not a GitHub Discussions board", repo)
	}
	owner, name, ok := strings.Cut(target.repo, "/")
	if !ok {
		return nil, fmt.Errorf("invalid repository %q", target.repo)
	}
	output, err := g.run(ctx, "api", "graphql", "-f", "query="+discussionsQuery, "-F", "owner="+owner, "-F", "name="+name)
	if err != nil {
		return nil, fmt.Errorf("list discussions for %s: %w: %s", target.repo, err, strings.TrimSpace(string(output)))
	}
	var response discussionsGraphQLResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode discussions for %s: %w", target.repo, err)
	}
	discussions := make([]Discussion, 0)
	for _, node := range response.Data.Repository.Discussions.Nodes {
		if node.Comments.TotalCount > 0 {
			continue
		}
		if !matchesDiscussionCategory(target.discussionCategory, node.Category.Slug, node.Category.Name) {
			continue
		}
		discussions = append(discussions, Discussion{Number: node.Number, Title: node.Title, Body: node.Body, CreatedAt: node.CreatedAt})
	}
	return discussions, nil
}

// matchesDiscussionCategory reports whether a thread belongs to the category a
// target names. GitHub's category slug ("q-a") is what appears in the board
// URL, but the display name ("Q&A") is what a person reads, so either is
// accepted, case-insensitively. An empty category matches every thread.
func matchesDiscussionCategory(want, slug, name string) bool {
	return want == "" || strings.EqualFold(want, slug) || strings.EqualFold(want, name)
}

type target struct {
	repo, owner, projectID string
	projectOwnerType       string
	isProject              bool
	isDiscussion           bool
	// discussionCategory is the slug of the single Discussions category a
	// discussion target watches. Empty means every category.
	discussionCategory string
}

// expandTargetShorthand rewrites the compact `projects:` and `discussions:`
// target forms into the canonical GitHub URLs parseTarget understands:
//
//	projects:OWNER/REPO/NUMBER      discussions:OWNER/REPO/CATEGORY
//	projects:NUMBER                 discussions:CATEGORY
//	                                discussions:OWNER/REPO
//
// The OWNER/REPO prefix may be omitted inside a git checkout, where it is
// taken from the repository the "origin" remote points at. Values without a
// recognized prefix are returned unchanged so full URLs and OWNER/REPO
// targets keep working.
func expandTargetShorthand(value string, originRepo func() (string, bool)) (string, error) {
	var kind, rest string
	switch {
	case strings.HasPrefix(value, "projects:"):
		kind, rest = "projects", strings.TrimPrefix(value, "projects:")
	case strings.HasPrefix(value, "discussions:"):
		kind, rest = "discussions", strings.TrimPrefix(value, "discussions:")
	default:
		return value, nil
	}
	rest = strings.Trim(rest, "/")
	parts := strings.Split(rest, "/")
	var repo, leaf string
	switch {
	case len(parts) == 3:
		repo, leaf = parts[0]+"/"+parts[1], parts[2]
	case len(parts) == 2 && kind == "discussions":
		repo = rest
	case len(parts) > 1:
		return "", fmt.Errorf("target %q must be %s:OWNER/REPO/NUMBER or %s:NUMBER", value, kind, kind)
	default:
		detected, ok := originRepo()
		if !ok {
			return "", fmt.Errorf("target %q omits OWNER/REPO and the current directory has no GitHub \"origin\" remote", value)
		}
		repo, leaf = detected, rest
	}
	if !validRepo(repo) {
		return "", fmt.Errorf("target %q does not name a valid OWNER/REPO", value)
	}
	if kind == "projects" {
		if leaf == "" || strings.Trim(leaf, "0123456789") != "" {
			return "", fmt.Errorf("target %q must end in a project number", value)
		}
		return "https://github.com/" + repo + "/projects/" + leaf, nil
	}
	if leaf == "" {
		return "https://github.com/" + repo + "/discussions", nil
	}
	return "https://github.com/" + repo + "/discussions/categories/" + leaf, nil
}

// originRemoteTarget returns the OWNER/REPO for the current directory's
// "origin" git remote, when it points to a GitHub repository.
func originRemoteTarget() (string, bool) {
	output, err := outputChildProcess(exec.Command("git", "remote", "get-url", "origin"))
	if err != nil {
		return "", false
	}
	return parseGitHubRemote(strings.TrimSpace(string(output)))
}

func parseGitHubRemote(remote string) (string, bool) {
	remote = strings.TrimSuffix(remote, ".git")
	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		repo := strings.TrimPrefix(remote, "git@github.com:")
		return repo, validRepo(repo)
	case strings.HasPrefix(remote, "ssh://git@github.com/"):
		repo := strings.TrimPrefix(remote, "ssh://git@github.com/")
		return repo, validRepo(repo)
	default:
		u, err := url.Parse(remote)
		if err != nil || u.Host != "github.com" {
			return "", false
		}
		repo := strings.Trim(u.Path, "/")
		return repo, validRepo(repo)
	}
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
	if len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] == "discussions" {
		return target{repo: parts[0] + "/" + parts[1], isDiscussion: true}, nil
	}
	if len(parts) == 5 && parts[0] != "" && parts[1] != "" && parts[2] == "discussions" && parts[3] == "categories" && parts[4] != "" {
		return target{repo: parts[0] + "/" + parts[1], isDiscussion: true, discussionCategory: parts[4]}, nil
	}
	return target{}, fmt.Errorf("target must be OWNER/REPO or a GitHub repository/project URL")
}

func projectListArgs(t target, filter string, allIssues bool) []string {
	args := []string{"project", "item-list", t.projectID, "--owner", t.owner, "--format", "json", "--limit", "1000"}
	return append(args, "--query", projectItemQuery(filter, allIssues))
}

func projectItemQuery(filter string, allIssues bool) string {
	query := "is:issue is:open"
	if !allIssues && filter != "" && filter != defaultIssueFilter {
		query += " " + filter
	}
	return query
}

func (g GHCLI) listProjectItems(ctx context.Context, target target, filter string, allIssues bool) ([]projectItem, error) {
	return g.listProjectItemFields(ctx, target, filter, allIssues, projectItemFields)
}

// projectItemFields is the issue selection a dispatching poll needs.
// projectItemKeyFields is the much smaller selection a board fingerprint
// needs: enough to notice an item appearing, disappearing, or changing
// status, and nothing else.
const (
	projectItemFields    = "number title body state createdAt repository{nameWithOwner} labels(first:100){nodes{name}}"
	projectItemKeyFields = "number state repository{nameWithOwner}"
)

func (g GHCLI) listProjectItemFields(ctx context.Context, target target, filter string, allIssues bool, contentFields string) ([]projectItem, error) {
	ownerField := target.projectOwnerType
	if ownerField == "users" {
		ownerField = "user"
	} else if ownerField == "orgs" {
		ownerField = "organization"
	} else {
		return nil, fmt.Errorf("unknown project owner type %q", target.projectOwnerType)
	}
	query := fmt.Sprintf(`query($login:String!,$number:Int!,$first:Int!,$after:String,$itemQuery:String!,$statusField:String!){
  %s(login:$login){
    projectV2(number:$number){
      items(first:$first,after:$after,query:$itemQuery){
        nodes{
          id
          fieldValueByName(name:$statusField){... on ProjectV2ItemFieldSingleSelectValue{name}}
          content{
            __typename
            ... on Issue{%s}
          }
        }
        pageInfo{hasNextPage endCursor}
      }
    }
  }
}`, ownerField, contentFields)

	var items []projectItem
	cursor := ""
	for len(items) < 1000 {
		args := []string{"api", "graphql", "-f", "query=" + query, "-F", "login=" + target.owner, "-F", "number=" + target.projectID, "-F", "first=100", "-F", "itemQuery=" + projectItemQuery(filter, allIssues), "-F", "statusField=Status"}
		if cursor != "" {
			args = append(args, "-F", "after="+cursor)
		}
		output, err := g.run(ctx, args...)
		if err != nil {
			return nil, projectItemListError(output, err)
		}
		var page projectItemsPage
		if err := json.Unmarshal(output, &page); err != nil {
			return nil, fmt.Errorf("decode project items: %w", err)
		}
		project := page.Data.User.Project
		if ownerField == "organization" {
			project = page.Data.Organization.Project
		}
		for _, item := range project.Items.Nodes {
			if len(items) == 1000 {
				break
			}
			converted := projectItem{ID: item.ID, Status: item.FieldValueByName.Name}
			if item.Content.Type == "Issue" {
				converted.Content = &projectContent{Issue: Issue{
					Number:     item.Content.Number,
					Repository: item.Content.Repository.NameWithOwner,
					Title:      item.Content.Title,
					Body:       item.Content.Body,
					State:      item.Content.State,
					CreatedAt:  item.Content.CreatedAt,
					Labels:     item.Content.Labels.Nodes,
				}, Type: "Issue"}
			}
			items = append(items, converted)
		}
		if !project.Items.PageInfo.HasNextPage {
			return items, nil
		}
		if project.Items.PageInfo.EndCursor == "" || project.Items.PageInfo.EndCursor == cursor {
			return nil, fmt.Errorf("list project items: pagination did not advance")
		}
		cursor = project.Items.PageInfo.EndCursor
	}
	return items, nil
}

// ProjectState returns a fingerprint of a project board's dispatchable state.
// It fetches only item IDs, statuses, and issue identities, skipping the issue
// bodies, labels, and the per-issue dependency lookups a dispatching poll
// performs, so push mode can check an idle board cheaply and often.
func (g GHCLI) ProjectState(ctx context.Context, repo string) (string, error) {
	target, err := parseTarget(repo)
	if err != nil {
		return "", err
	}
	if !target.isProject {
		return "", fmt.Errorf("project state requires a project target, got %q", repo)
	}
	var items []projectItem
	if target.projectOwnerType != "" {
		items, err = g.listProjectItemFields(ctx, target, g.Filter, g.AllIssues, projectItemKeyFields)
	} else {
		items, err = decodeProjectItems(g.run(ctx, projectListArgs(target, g.Filter, g.AllIssues)...))
	}
	if err != nil {
		return "", err
	}
	return projectItemsFingerprint(items), nil
}

// projectItemsFingerprint hashes the board-visible identity and status of
// every item, ordered independently of how GitHub returned them so that a
// reshuffled response alone never looks like a change.
func projectItemsFingerprint(items []projectItem) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		line := item.ID + "\x00" + item.Status
		if item.Content != nil {
			line += fmt.Sprintf("\x00%s\x00%s#%d\x00%s", item.Content.Type, item.Content.Repository, item.Content.Number, item.Content.State)
		}
		lines = append(lines, line)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

func projectItemListError(output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if strings.Contains(detail, "missing required scopes") && strings.Contains(detail, "read:project") {
		return fmt.Errorf("list project items: %w; authenticate with the read:project scope using `gh auth refresh -s read:project`", err)
	}
	if detail != "" {
		return fmt.Errorf("list project items: %w: %s", err, detail)
	}
	return fmt.Errorf("list project items: %w", err)
}

var dependencyPattern = regexp.MustCompile(`(?i)\bdepends\s+on\s+#(\d+)`)

func (g GHCLI) loadDependencies(ctx context.Context, repo string, issue *Issue) error {
	dependencies := make(map[int]IssueDependency)
	public := repo != "" && g.isPublicRepo(ctx, repo)
	for _, match := range dependencyPattern.FindAllStringSubmatch(issue.Body, -1) {
		number := 0
		if _, err := fmt.Sscanf(match[1], "%d", &number); err == nil && number > 0 {
			dependency := IssueDependency{Number: number}
			if repo != "" {
				output, viewErr := g.dependencyIssueView(ctx, public, repo, number)
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
		output, err := g.apiGET(ctx, public, "repos/"+repo+"/issues/"+fmt.Sprint(issue.Number)+"/dependencies/blocked_by", "--header", "X-GitHub-Api-Version: 2022-11-28")
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

func (g GHCLI) PostComment(ctx context.Context, repo string, number int, body string) error {
	output, err := g.run(ctx, "api", "repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments", "-f", "body="+body)
	if err != nil {
		return fmt.Errorf("post comment on #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (g GHCLI) ListComments(ctx context.Context, repo string, number int) ([]Comment, error) {
	public := g.isPublicRepo(ctx, repo)
	output, err := g.apiGETPaginated(ctx, public, "repos/"+repo+"/issues/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return nil, fmt.Errorf("list comments on #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
	}
	var raw []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("decode comments on #%d: %w", number, err)
	}
	comments := make([]Comment, len(raw))
	for i, comment := range raw {
		comments[i] = Comment{Body: comment.Body, CreatedAt: comment.CreatedAt}
	}
	return comments, nil
}

func (g GHCLI) SetIssueStatus(ctx context.Context, repo string, issue Issue, status string) error {
	parsedTarget, err := parseTarget(repo)
	if err != nil {
		return err
	}
	if !parsedTarget.isProject {
		items, err := g.repositoryProjectItems(ctx, parsedTarget.repo, issue.Number)
		if err != nil {
			return err
		}
		for _, item := range items {
			projectTarget := target{
				owner:     item.Project.Owner.Login,
				projectID: strconv.Itoa(item.Project.Number),
				isProject: true,
			}
			if err := g.setProjectItemStatus(ctx, projectTarget, item.ID, issue.Number, status); err != nil {
				return err
			}
		}
		return nil
	}

	itemID := issue.ProjectItemID
	if itemID == "" {
		output, err := g.run(ctx, projectListArgs(parsedTarget, "", true)...)
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
		return fmt.Errorf("%w: issue #%d is not in project %s", errProjectIssueNotFound, issue.Number, parsedTarget.projectID)
	}
	return g.setProjectItemStatus(ctx, parsedTarget, itemID, issue.Number, status)
}

func (g GHCLI) repositoryProjectItems(ctx context.Context, repo string, number int) ([]repositoryProjectItem, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repository %q", repo)
	}
	const query = `query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){
  repository(owner:$owner,name:$repo){
    issue(number:$number){
      projectItems(first:100,after:$endCursor){
        nodes{id project{id number owner{... on User{login} ... on Organization{login}}}}
        pageInfo{hasNextPage endCursor}
      }
    }
  }
}`
	var items []repositoryProjectItem
	cursor := ""
	for {
		args := []string{"api", "graphql", "-f", "query=" + query, "-F", "owner=" + parts[0], "-F", "repo=" + parts[1], "-F", fmt.Sprintf("number=%d", number)}
		if cursor != "" {
			args = append(args, "-F", "endCursor="+cursor)
		}
		output, err := g.run(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("list projects for issue #%d: %w: %s", number, err, strings.TrimSpace(string(output)))
		}
		var page repositoryProjectItemsPage
		if err := json.Unmarshal(output, &page); err != nil {
			return nil, fmt.Errorf("decode projects for issue #%d: %w", number, err)
		}
		projectItems := page.Data.Repository.Issue.ProjectItems
		items = append(items, projectItems.Nodes...)
		if !projectItems.PageInfo.HasNextPage {
			return items, nil
		}
		if projectItems.PageInfo.EndCursor == "" || projectItems.PageInfo.EndCursor == cursor {
			return nil, fmt.Errorf("list projects for issue #%d: pagination did not advance", number)
		}
		cursor = projectItems.PageInfo.EndCursor
	}
}

func (g GHCLI) setProjectItemStatus(ctx context.Context, target target, itemID string, issueNumber int, status string) error {

	viewOutput, err := g.run(ctx, "project", "view", target.projectID, "--owner", target.owner, "--format", "json")
	if err != nil {
		return fmt.Errorf("view project: %w: %s", err, strings.TrimSpace(string(viewOutput)))
	}
	var view projectView
	if err := json.Unmarshal(viewOutput, &view); err != nil {
		return fmt.Errorf("decode project: %w", err)
	}
	if view.ID == "" {
		return fmt.Errorf("project %s has no ID", target.projectID)
	}

	fieldsOutput, err := g.run(ctx, "project", "field-list", target.projectID, "--owner", target.owner, "--format", "json", "--limit", "1000")
	fields, err := decodeProjectFields(fieldsOutput, err)
	if err != nil {
		return err
	}
	fieldID, optionID := projectStatusOption(fields, status, g.ReadyState == "")
	if fieldID == "" || optionID == "" {
		return fmt.Errorf("project %s has no Status option %q", target.projectID, status)
	}

	if output, err := g.run(ctx, "project", "item-edit", "--id", itemID, "--field-id", fieldID, "--project-id", view.ID, "--single-select-option-id", optionID); err != nil {
		return projectStatusError(issueNumber, err, strings.TrimSpace(string(output)))
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
	Binary, CodexBinary, ClaudeBinary string
	// Agents holds every configured agent spec (agent/model:level) to load
	// balance across when dispatching new issues. AgentName rotates through it
	// round robin.
	Agents      []string
	Agent, Repo string
	Output      io.Writer
	Yolo        bool
	// agentCursor is shared across copies of CommandRunner (via pointer) so
	// round robin selection advances consistently regardless of how many
	// times the struct is copied.
	agentCursor *atomic.Uint64
}

func commandArgs(r CommandRunner, issue Issue) []string {
	return commandArgsForSession(r, issue, AgentSession{})
}

func commandArgsForSession(r CommandRunner, issue Issue, session AgentSession) []string {
	target := issue.Target
	if target == "" {
		target = r.Repo
	}
	prompt := "continue"
	if !session.Resume {
		if isDiscussionTarget(target) {
			prompt = fmt.Sprintf("/gh-discuss %d", issue.Number)
		} else {
			prompt = fmt.Sprintf("/gh-fix %d", issue.Number)
		}
		if repo := issueRepository(target, issue); repo != "" {
			prompt += "\n\nRepository: " + repo
		}
		prompt += "\n\nKeep your responses concise. Do not include code diffs or large code blocks; summarize the changes and tests instead."
	} else {
		prompt += "\n\nRecover the existing work. If this issue has a draft pull request, inspect it and pull its branch before continuing."
		if session.CheckoutDirectory != "" {
			if _, err := os.Stat(session.CheckoutDirectory); os.IsNotExist(err) {
				prompt += "\n\nThe original repository directory no longer exists. Regenerate any missing work before continuing."
			}
		}
	}
	spec := r.specForSession(session)
	if spec.Name == "codex" {
		args := []string{"exec"}
		if session.Resume {
			args = append(args, "resume")
		}
		if r.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		if !session.Resume && spec.Model != "" {
			args = append(args, "--model", spec.Model)
		}
		if !session.Resume && spec.Level != "" {
			args = append(args, "-c", "model_reasoning_effort="+spec.Level)
		}
		if session.Resume {
			args = append(args, session.ID)
		}
		return append(args, prompt)
	}
	args := []string{"-p"}
	if session.Resume {
		args = append(args, "--resume", session.ID)
	} else if session.ID != "" {
		args = append(args, "--session-id", session.ID)
	}
	if r.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		// Print mode cannot prompt for tool approval. Let Claude make its normal
		// permission decisions autonomously instead of silently denying the
		// shell commands the issue workflow needs and exiting successfully.
		args = append(args, "--permission-mode", "auto")
	}
	if !session.Resume && spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if !session.Resume && spec.Level != "" {
		args = append(args, "--effort", spec.Level)
	}
	// Claude's default text output only prints once the full response is
	// ready. Stream JSON events instead so the dashboard shows live progress
	// the same way Codex's plain-text output already does.
	args = append(args, "--output-format", "stream-json", "--verbose")
	return append(args, prompt)
}

func (r CommandRunner) Run(ctx context.Context, issue Issue) error {
	return r.run(ctx, issue, AgentSession{}, nil, nil)
}

func (r CommandRunner) RunWithOutput(ctx context.Context, issue Issue, output io.Writer) error {
	return r.run(ctx, issue, AgentSession{}, nil, output)
}

// specForSession resolves the agent, model, and level to run with. A resumed
// or dispatched session carries the spec it was assigned so load balanced runs
// keep the model they started with.
func (r CommandRunner) specForSession(session AgentSession) agentSpec {
	value := session.Agent
	if value == "" {
		value = r.Agent
	}
	spec, err := parseAgentSpec(value)
	if err != nil {
		return agentSpec{Name: value}
	}
	return spec
}

// AgentName returns the agent spec (agent/model:level) to use for the next
// newly dispatched issue, rotating round robin through Agents when more than
// one is configured so work is load balanced evenly across concurrency.
func (r CommandRunner) AgentName() string {
	if len(r.Agents) == 0 {
		if r.Agent != "" {
			return r.Agent
		}
		return "codex"
	}
	if len(r.Agents) == 1 || r.agentCursor == nil {
		return r.Agents[0]
	}
	index := r.agentCursor.Add(1) - 1
	return r.Agents[index%uint64(len(r.Agents))]
}

func (r CommandRunner) RunSession(ctx context.Context, issue Issue, session AgentSession, updateSession func(AgentSession)) error {
	return r.run(ctx, issue, session, updateSession, nil)
}

func (r CommandRunner) RunSessionWithOutput(ctx context.Context, issue Issue, session AgentSession, updateSession func(AgentSession), output io.Writer) error {
	return r.run(ctx, issue, session, updateSession, output)
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

type sessionMetadataCaptureWriter struct {
	mu               sync.Mutex
	output           io.Writer
	buffer           []byte
	onUpdate         func(AgentSession)
	captureSession   bool
	sessionCaptured  bool
	checkoutCaptured bool
}

var codexSessionIDPattern = regexp.MustCompile(`(?i)session id:\s*([0-9a-f]{8}-[0-9a-f-]{27,})`)

const checkoutDirectoryMarker = "GLORP_CHECKOUT_DIRECTORY="

func (w *sessionMetadataCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.output.Write(p); err != nil {
		return 0, err
	}
	w.buffer = append(w.buffer, p...)
	for {
		newline := bytes.IndexByte(w.buffer, '\n')
		if newline < 0 {
			break
		}
		w.captureLine(string(w.buffer[:newline]))
		w.buffer = w.buffer[newline+1:]
	}
	return len(p), nil
}

func (w *sessionMetadataCaptureWriter) captureLine(line string) {
	if w.captureSession && !w.sessionCaptured {
		match := codexSessionIDPattern.FindStringSubmatch(line)
		if len(match) == 2 {
			w.sessionCaptured = true
			w.onUpdate(AgentSession{ID: match[1]})
		}
	}
	if w.checkoutCaptured {
		return
	}
	marker := strings.Index(line, checkoutDirectoryMarker)
	if marker < 0 {
		return
	}
	checkout := strings.TrimSpace(line[marker+len(checkoutDirectoryMarker):])
	checkout = strings.Trim(checkout, "`*\"' ")
	if !filepath.IsAbs(checkout) {
		return
	}
	info, err := os.Stat(checkout)
	if err != nil || !info.IsDir() {
		return
	}
	w.checkoutCaptured = true
	w.onUpdate(AgentSession{CheckoutDirectory: checkout})
}

func (w *sessionMetadataCaptureWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) != 0 {
		w.captureLine(string(w.buffer))
		w.buffer = nil
	}
}

// claudeJSONOutputWriter decodes Claude's --output-format stream-json events
// into readable text lines. Claude's default text output only prints once
// the full response is ready, which leaves the dashboard showing no output
// at all until the agent finishes; stream-json events arrive incrementally.
type claudeJSONOutputWriter struct {
	mu     sync.Mutex
	output io.Writer
	buffer []byte
}

type claudeStreamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

func newClaudeJSONOutputWriter(output io.Writer) *claudeJSONOutputWriter {
	return &claudeJSONOutputWriter{output: output}
}

func (w *claudeJSONOutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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

func (w *claudeJSONOutputWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) == 0 {
		return nil
	}
	line := append([]byte(nil), w.buffer...)
	w.buffer = nil
	return w.writeLine(line)
}

func (w *claudeJSONOutputWriter) writeLine(line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var event claudeStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		_, err = fmt.Fprintln(w.output, string(line))
		return err
	}
	var texts []string
	switch {
	case event.Type == "assistant":
		for _, block := range event.Message.Content {
			switch block.Type {
			case "text":
				if text := strings.TrimSpace(block.Text); text != "" {
					texts = append(texts, text)
				}
			case "tool_use":
				texts = append(texts, "Running: "+claudeToolUseSummary(block.Name, block.Input))
			}
		}
	case event.Type == "result" && event.IsError:
		if text := strings.TrimSpace(event.Result); text != "" {
			texts = append(texts, "Agent error: "+text)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(w.output, strings.Join(texts, "\n"))
	return err
}

// claudeToolUseDetailKeys are the tool input fields that carry enough context
// to make a progress line useful, most specific first. A bare "Running: Read"
// says nothing about what the agent is doing; "Running: Read main.go" does.
var claudeToolUseDetailKeys = []string{
	"command", "file_path", "notebook_path", "pattern", "url", "query",
	"path", "description", "prompt", "skill",
}

// claudeToolUseDetailPathKeys hold filesystem paths, which are shortened from
// the front so the filename survives truncation.
var claudeToolUseDetailPathKeys = map[string]bool{"file_path": true, "notebook_path": true, "path": true}

const claudeToolUseDetailLimit = 120

func claudeToolUseSummary(name string, input json.RawMessage) string {
	key, detail := claudeToolUseDetail(input)
	switch {
	case detail == "":
		return name
	case key == "command":
		// Shell commands are self-describing; the tool name adds nothing.
		return detail
	case name == "":
		return detail
	}
	return name + " " + detail
}

// claudeToolUseDetail returns the most descriptive string field of a tool's
// input along with the key it came from, or empty strings when the input has
// nothing worth showing.
func claudeToolUseDetail(input json.RawMessage) (string, string) {
	if len(input) == 0 {
		return "", ""
	}
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return "", ""
	}
	for _, key := range claudeToolUseDetailKeys {
		value, ok := fields[key].(string)
		if !ok {
			continue
		}
		if value = strings.TrimSpace(strings.Join(strings.Fields(value), " ")); value == "" {
			continue
		}
		return key, truncateToolUseDetail(value, claudeToolUseDetailPathKeys[key])
	}
	return "", ""
}

func truncateToolUseDetail(value string, isPath bool) string {
	runes := []rune(value)
	if len(runes) <= claudeToolUseDetailLimit {
		return value
	}
	if isPath {
		return "…" + string(runes[len(runes)-claudeToolUseDetailLimit:])
	}
	return string(runes[:claudeToolUseDetailLimit]) + "…"
}

func (r CommandRunner) binary(agent string) string {
	if agent == "codex" && r.CodexBinary != "" {
		return r.CodexBinary
	}
	if agent == "claude" && r.ClaudeBinary != "" {
		return r.ClaudeBinary
	}
	return r.Binary
}

// missingSessionPatterns matches the messages agents print when asked to
// resume a session they no longer have on disk (sessions expire, and a glorp
// work state file routinely outlives the agent's local conversation history).
var missingSessionPatterns = []string{
	"no conversation found",
	"no session found",
	"session not found",
	"could not find session",
	"unable to find session",
}

// missingSessionDetector passes agent output straight through while watching
// for a "that session does not exist" message so a failed resume can restart
// the work instead of being reported as an agent failure. It keeps a small
// tail of the previous chunk so a message split across writes still matches.
type missingSessionDetector struct {
	mu      sync.Mutex
	output  io.Writer
	tail    string
	missing bool
}

const missingSessionTailBytes = 128

func (d *missingSessionDetector) Write(p []byte) (int, error) {
	d.mu.Lock()
	if !d.missing {
		window := strings.ToLower(d.tail + string(p))
		for _, pattern := range missingSessionPatterns {
			if strings.Contains(window, pattern) {
				d.missing = true
				break
			}
		}
		if len(window) > missingSessionTailBytes {
			window = window[len(window)-missingSessionTailBytes:]
		}
		d.tail = window
	}
	d.mu.Unlock()
	return d.output.Write(p)
}

func (d *missingSessionDetector) sessionMissing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.missing
}

func (r CommandRunner) run(ctx context.Context, issue Issue, session AgentSession, updateSession func(AgentSession), jobOutput io.Writer) error {
	runErr, sessionMissing := r.runOnce(ctx, issue, session, updateSession, jobOutput)
	if runErr == nil || !sessionMissing {
		return runErr
	}
	// The recorded session is gone from the agent's local history, so the
	// resume can never succeed. Start the work over rather than reporting a
	// failure nobody can act on; the issue workflow is re-entrant and adopts
	// the existing draft pull request.
	session.Resume = false
	if r.specForSession(session).Name == "codex" {
		// Codex assigns its own session IDs and reports the new one on stdout.
		session.ID = ""
	}
	runErr, _ = r.runOnce(ctx, issue, session, updateSession, jobOutput)
	return runErr
}

func (r CommandRunner) runOnce(ctx context.Context, issue Issue, session AgentSession, updateSession func(AgentSession), jobOutput io.Writer) (error, bool) {
	agent := r.specForSession(session).Name
	args := commandArgsForSession(r, issue, session)
	cmd := newAgentCommand(ctx, r.binary(agent), args...)
	if session.CheckoutDirectory != "" {
		if info, err := os.Stat(session.CheckoutDirectory); err == nil && info.IsDir() {
			cmd.Dir = session.CheckoutDirectory
		}
	}
	agentOutput := io.Writer(io.Discard)
	if jobOutput != nil {
		agentOutput = io.MultiWriter(agentOutput, jobOutput)
	}
	if r.Output != nil {
		agentOutput = io.MultiWriter(agentOutput, r.Output)
	}
	var metadataOutput *sessionMetadataCaptureWriter
	if updateSession != nil {
		metadataOutput = &sessionMetadataCaptureWriter{
			output: agentOutput, onUpdate: updateSession,
			captureSession: agent == "codex" && !session.Resume,
		}
		agentOutput = metadataOutput
	}
	var claudeOutput *claudeJSONOutputWriter
	if agent == "claude" {
		claudeOutput = newClaudeJSONOutputWriter(agentOutput)
		agentOutput = claudeOutput
	}
	var detector *missingSessionDetector
	if session.Resume {
		detector = &missingSessionDetector{output: agentOutput}
		agentOutput = detector
	}
	cmd.Stdout, cmd.Stderr = agentOutput, agentOutput
	runErr := runChildProcess(cmd)
	if claudeOutput != nil {
		if err := claudeOutput.Flush(); runErr == nil && err != nil {
			runErr = err
		}
	}
	if metadataOutput != nil {
		metadataOutput.Flush()
	}
	if runErr != nil {
		if detector != nil && detector.sessionMissing() {
			fmt.Fprintf(agentOutput, "Session %s no longer exists; restarting the work from scratch.\n", session.ID)
			return runErr, true
		}
		repo := r.Repo
		if issue.Target != "" {
			repo = issueRepository(issue.Target, issue)
		}
		report, reportErr := bugReportURL(repo, issue, args)
		if reportErr != nil {
			return fmt.Errorf("agent failed: %w (could not create bug report URL: %v)", runErr, reportErr), false
		}
		return fmt.Errorf("agent failed: %w; bug report: %s", runErr, report), false
	}
	return nil, false
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
