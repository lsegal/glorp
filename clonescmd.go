package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/lsegal/glorp/agents"
	"github.com/lsegal/glorp/core"
)

// clonesUsage is the help text of `glorp clones` (issue #830).
const clonesUsage = `Usage: glorp clones [flags] [TARGET [TARGET ...]]

List every clone glorp has made for a job, with its repository, issue, and
pull request, and whether it is still in use. Clones already on disk under the
temp directories are adopted into the list. With -clear, remove the unused ones
and report the space reclaimed.

A clone is in use while its job is active, waiting, queued, or paused; a clone
whose status cannot be verified is kept and the reason is shown. Pass the same
TARGETs and -state as "glorp watch" so the work state is read the same way; with
no TARGET, the current directory's "origin" GitHub remote is used.

The temp directories come from -temp-dir, or else from the config file's
"temp-dir" setting, or else the system temp directory.

Flags:`

func clonesFlagSet(tempDirs *tempDirFlag) *flag.FlagSet {
	flags := flag.NewFlagSet("clones", flag.ContinueOnError)
	flags.Bool("clear", false, "remove every unused clone and report the space reclaimed")
	flags.String("state", ".glorp.json", "work state file of the glorp watch whose clones to list")
	flags.String("config", agents.DefaultConfigPath, "config file whose \"temp-dir\" setting names the temp directories")
	flags.Var(tempDirs, "temp-dir", "temp directory clones are placed in (repeatable; overrides the config file)")
	return flags
}

func runClonesCommand(args []string) int {
	return clonesCommand(args, os.Stdout, os.Stderr, originRemoteTarget)
}

func clonesCommand(args []string, stdout, stderr io.Writer, origin func() (string, bool)) int {
	tempDirs := &tempDirFlag{}
	flags := clonesFlagSet(tempDirs)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, clonesUsage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	statePath := flagValue[string](flags, "state")
	dirs := tempDirs.values
	if len(dirs) == 0 {
		configured, err := configTempDirs(flagValue[string](flags, "config"))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		dirs = configured
	}
	targets := flags.Args()
	if len(targets) == 0 {
		if repo, ok := origin(); ok {
			targets = []string{repo}
		}
	}
	legacy := stateHasLegacyKeys(statePath)
	if len(targets) != 1 && legacy {
		fmt.Fprintf(stderr, "%s records work by issue number only; pass the one TARGET its glorp watch was started with\n", statePath)
		return 2
	}
	// Without bare-number keys every key names its target, so all of them
	// are read: a clone of a target not named here is still protected.
	stateTargets := targets
	if !legacy {
		stateTargets = nil
	}
	reg, err := loadCloneRegistry(clonesPathFor(statePath))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	roots := cloneRoots(dirs)
	usage := func() cloneUsage {
		work, workErr := loadScopedWorkState(statePath, stateTargets)
		return cloneUsage{roots: roots, work: work, workErr: workErr}
	}
	first := usage()
	reg.adopt(roots, first.work)
	if flagValue[bool](flags, "clear") {
		printClearClones(stdout, clearClones(reg, usage))
		return 0
	}
	printClones(stdout, listClones(reg, first))
	return 0
}

// configTempDirs reads the "temp-dir" setting from the config file.
func configTempDirs(path string) ([]string, error) {
	settings, err := loadConfigSettings(path)
	if err != nil {
		return nil, err
	}
	raw, ok := settings["temp-dir"]
	if !ok {
		return nil, nil
	}
	values, err := configSettingValues(raw)
	if err != nil {
		return nil, fmt.Errorf("config %s: \"temp-dir\": %w", path, err)
	}
	return normalizeTempDirs(values)
}

// stateHasLegacyKeys reports whether a state file keys work by bare issue
// number, which only a single-target watch writes and only its target can
// interpret.
func stateHasLegacyKeys(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return false
	}
	for key := range keys {
		if _, err := strconv.Atoi(key); err == nil {
			return true
		}
	}
	return false
}

func cloneLabel(entry core.CloneEntry) string {
	var parts []string
	if entry.Repository != "" && entry.Issue != 0 {
		parts = append(parts, fmt.Sprintf("%s#%d", entry.Repository, entry.Issue))
	} else if entry.Job != "" {
		parts = append(parts, entry.Job)
	}
	if entry.PullRequest != 0 {
		repo := entry.PullRequestRepository
		if repo == "" {
			repo = entry.Repository
		}
		parts = append(parts, fmt.Sprintf("PR https://github.com/%s/pull/%d", repo, entry.PullRequest))
	}
	return strings.Join(parts, ", ")
}

func printClones(out io.Writer, snapshot core.ClonesSnapshot) {
	fmt.Fprintf(out, "temp directories: %s\n", strings.Join(snapshot.Roots, ", "))
	unused := 0
	for _, entry := range snapshot.Clones {
		if !entry.InUse {
			unused++
		}
	}
	fmt.Fprintf(out, "%d clone(s), %d unused\n", len(snapshot.Clones), unused)
	for _, entry := range snapshot.Clones {
		status := "unused"
		if entry.InUse {
			status = "in use"
		}
		fmt.Fprintf(out, "- [%s] %s\n", status, entry.Path)
		if label := cloneLabel(entry); label != "" {
			fmt.Fprintf(out, "    %s\n", label)
		}
		if entry.Reason != "" {
			fmt.Fprintf(out, "    %s\n", entry.Reason)
		}
	}
}

func printClearClones(out io.Writer, result core.ClearClonesResult) {
	for _, entry := range result.Removed {
		fmt.Fprintf(out, "removed %s\n", entry.Path)
	}
	for _, failure := range result.Errors {
		fmt.Fprintf(out, "could not remove %s\n", failure)
	}
	fmt.Fprintf(out, "removed %d clone(s), kept %d, reclaimed %s\n", len(result.Removed), result.Kept, formatBytes(result.Reclaimed))
}
