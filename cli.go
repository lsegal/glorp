package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

// command is one top-level `glorp <name>` subcommand.
type command struct {
	name    string
	summary string
	usage   string
	// run executes the command with the arguments following its name and
	// returns the process exit code.
	run func(args []string) int
}

// commands is the full subcommand table, in the order help prints them. It is
// populated in init because help and the per-command usage handlers look
// commands up in the same table they belong to.
var commands []command

func init() {
	commands = []command{
		{
			name:    "watch",
			summary: "Watch GitHub targets and dispatch agents for ready issues",
			usage: `Usage: glorp watch [flags] [TARGET [TARGET ...]]

Watch GitHub repositories, project boards, and discussion boards, dispatching
an agent for every issue that becomes ready. If no TARGET is given, glorp uses
the current directory's "origin" git remote when it points to GitHub.

With -browser, glorp reads GitHub through a headless Chrome it signs in once
and reuses, instead of the GitHub API. Browser mode always polls: no webhook
server and no ngrok tunnel are started, and the poll interval defaults to 5s.
-browser-vision additionally lets an agent recover an issue list or a project
board from a single screenshot when GitHub's markup changes, under one hard
per-run budget shared by both.
-no-headless drives that same browser visibly instead, so the pages glorp reads
can be watched while it reads them when a browser-mode run needs debugging.

Flags:`,
			run: runWatch,
		},
		{
			name:    "ui",
			summary: "Open a running glorp dashboard in a browser",
			usage: `Usage: glorp ui [flags]

Find glorp dashboards listening on localhost and open one in a browser. When
several instances are running, an interactive picker chooses between them; on a
non-interactive terminal the lowest-numbered port is opened.

Flags:`,
			run: runUICommand,
		},
		{
			name:    "auth",
			summary: "Sign glorp's browser profile in to GitHub for -browser mode",
			usage: `Usage: glorp auth [flags]

Sign the browser profile that "glorp watch -browser" reads GitHub with in to
GitHub. glorp opens a normal browser window on its own profile at GitHub's login
page and waits until the sign-in finishes, then closes it again; the session is
stored in the profile and survives later watch runs.

Chrome allows one process per profile directory, so stop a running
"glorp watch -browser" (or pass a different -browser-profile) before signing in.

Flags:`,
			run: runAuthCommand,
		},
		{
			name:    "version",
			summary: "Print the glorp version",
			usage:   "Usage: glorp version",
			run:     runVersion,
		},
		{
			name:    "upgrade",
			summary: "Upgrade glorp to the latest release",
			usage: `Usage: glorp upgrade

Re-run the published installer for this platform to replace glorp with the
latest release.`,
			run: runUpgradeCommand,
		},
		{
			name:    "help",
			summary: "Show help for glorp or one of its commands",
			usage:   "Usage: glorp help [command]",
			run:     runHelp,
		},
	}
}

func lookupCommand(name string) (command, bool) {
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd, true
		}
	}
	return command{}, false
}

// normalizeCommand maps the flag spellings people reach for out of habit onto
// the subcommands that replaced them, so `glorp --version` and `glorp -h` keep
// working after the top-level flag parser was removed.
func normalizeCommand(name string) string {
	switch strings.TrimLeft(name, "-") {
	case "version", "v":
		return "version"
	case "help", "h":
		return "help"
	}
	return name
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stderr))
}

func runCLI(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	name := normalizeCommand(args[0])
	cmd, ok := lookupCommand(name)
	if !ok {
		fmt.Fprintf(stderr, "glorp: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
	return cmd.run(args[1:])
}

func printUsage(out io.Writer) {
	fmt.Fprint(out, `glorp watches GitHub repositories, project boards, and discussion boards and
dispatches a coding agent for each issue that is ready to work on.

Usage: glorp <command> [arguments]

Commands:
`)
	width := 0
	for _, cmd := range commands {
		if len(cmd.name) > width {
			width = len(cmd.name)
		}
	}
	for _, cmd := range commands {
		fmt.Fprintf(out, "  %-*s  %s\n", width, cmd.name, cmd.summary)
	}
	fmt.Fprint(out, "\nRun \"glorp help <command>\" for more information about a command.\n")
}

func runHelp(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: glorp help [command]")
		return 2
	}
	cmd, ok := lookupCommand(normalizeCommand(args[0]))
	if !ok {
		fmt.Fprintf(os.Stderr, "glorp: unknown command %q\n\n", args[0])
		printUsage(os.Stderr)
		return 2
	}
	fmt.Fprintln(os.Stdout, cmd.usage)
	if flags := commandFlags(cmd.name); flags != nil {
		flags.SetOutput(os.Stdout)
		flags.PrintDefaults()
	}
	return 0
}

func runVersion(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: glorp version")
		return 2
	}
	fmt.Println(version)
	return 0
}

func runUpgradeCommand(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: glorp upgrade")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo := upgradeRepo(os.Getenv)
	if err := runUpgrade(ctx, os.Stdout, version, func(ctx context.Context) (string, error) {
		return latestReleaseTag(ctx, doPublicGitHubRequest, repo)
	}, func(ctx context.Context) *exec.Cmd {
		return upgradeCommand(ctx, runtime.GOOS, repo)
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
