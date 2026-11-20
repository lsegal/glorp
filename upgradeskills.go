package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lsegal/glorp/agents"
)

const upgradeSkillsUsage = `Usage: glorp upgrade-skills [flags]

Reinstall glorp's gh-fix, gh-discuss, gh-create, and gh-safe-merge skills
through skills.sh for every agent's skills target, without upgrading glorp
itself. Use it when the installed skills are stale or damaged and you do not
want a binary upgrade and restart.

The skills are installed from this glorp's own release, so they match what the
startup check expects; a development build installs the latest release's
skills instead. Nothing is downloaded but the skills, and the glorp binary is
left as it is.

Flags:`

// upgradeSkillsOptions are the flags `glorp upgrade-skills` takes.
type upgradeSkillsOptions struct {
	config string
}

func upgradeSkillsFlagSet(opts *upgradeSkillsOptions) *flag.FlagSet {
	flags := flag.NewFlagSet("upgrade-skills", flag.ExitOnError)
	flags.StringVar(&opts.config, "config", agents.DefaultConfigPath, "agent definition file read alongside the built-in agents")
	return flags
}

func runUpgradeSkillsCommand(args []string) int {
	var opts upgradeSkillsOptions
	flags := upgradeSkillsFlagSet(&opts)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: glorp upgrade-skills [flags]")
		return 2
	}
	registry, err := agents.Load(opts.config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	repo := upgradeRepo(os.Getenv)
	err = runUpgradeSkills(ctx, os.Stdout, version, registry.SkillsTargets(), func(ctx context.Context) (string, error) {
		return latestReleaseTag(ctx, doPublicGitHubRequest, repo)
	}, skillsRefresher{Repo: repo, Run: npxCommand}.Refresh)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runUpgradeSkills reinstalls glorp's skills for targets without touching the
// binary (issue #872). A release build installs its own tag's skills, the
// copies the startup check compares against, so the next start does not
// reinstall them again; a development build has no tag of its own and
// installs the latest release's.
func runUpgradeSkills(ctx context.Context, out io.Writer, currentVersion string, targets []string, latestTag func(context.Context) (string, error), refresh func(ctx context.Context, tag string, targets []string) error) error {
	var tag string
	if _, ok := parseReleaseVersion(currentVersion); ok {
		tag = releaseTag(currentVersion)
	} else {
		latest, err := latestTag(ctx)
		if err != nil {
			return fmt.Errorf("upgrade skills: find the latest release: %w", err)
		}
		tag = latest
	}
	fmt.Fprintf(out, "Reinstalling skills %s from glorp %s for %s...\n", strings.Join(glorpSkills, ", "), tag, strings.Join(targets, ", "))
	if err := refresh(ctx, tag, targets); err != nil {
		return fmt.Errorf("upgrade skills: %w", err)
	}
	fmt.Fprintf(out, "Reinstalled skills %s from glorp %s for %s.\n", strings.Join(glorpSkills, ", "), tag, strings.Join(targets, ", "))
	return nil
}
