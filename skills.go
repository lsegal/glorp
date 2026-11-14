package main

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/lsegal/glorp/process"
)

// bundledSkills is the gh-fix, gh-discuss, and gh-create skill text this
// binary was built with. The installed copies agents load are compared
// against it, so a self-updated glorp notices skills it has outgrown (issue
// #797).
//
//go:embed .agents/skills
var bundledSkills embed.FS

// bundledSkillsRoot is where the skills sit inside bundledSkills and in the
// repository the installers and refresh install them from.
const bundledSkillsRoot = ".agents/skills"

// glorpSkills are the skills the installers install and glorp keeps current,
// in the order install.ps1 and install.sh add them.
var glorpSkills = []string{"gh-fix", "gh-discuss", "gh-create"}

// installedSkillsDir is where `npx skills add --global` keeps the copy of
// each skill that every agent's skills directory links to.
func installedSkillsDir(home string) string {
	return filepath.Join(home, ".agents", "skills")
}

// staleSkills reports which of glorp's skills under dir are missing or differ
// from the copies in bundle. Line endings are ignored, since a skills.sh
// clone on Windows may check the files out with CRLF.
func staleSkills(bundle fs.FS, dir string) []string {
	var stale []string
	for _, name := range glorpSkills {
		root := path.Join(bundledSkillsRoot, name)
		current := true
		err := fs.WalkDir(bundle, root, func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			want, err := fs.ReadFile(bundle, file)
			if err != nil {
				return err
			}
			have, err := os.ReadFile(filepath.Join(dir, name, filepath.FromSlash(strings.TrimPrefix(file, root+"/"))))
			if err != nil || !bytes.Equal(normalizeNewlines(have), normalizeNewlines(want)) {
				current = false
				return fs.SkipAll
			}
			return nil
		})
		if err != nil || !current {
			stale = append(stale, name)
		}
	}
	return stale
}

func normalizeNewlines(text []byte) []byte {
	return bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
}

// skillSource is the skills.sh source for one skill at a release tag. It is
// pinned to the tag rather than the default branch the installers read, so
// the refreshed copy is exactly the one the binary was built with and the
// next currency check finds it current.
func skillSource(repo, tag, skill string) string {
	return fmt.Sprintf("https://github.com/%s/tree/%s/%s/%s", repo, tag, bundledSkillsRoot, skill)
}

// skillsAddArgs is the npx command line that installs one skill globally for
// every target, in the shape install.ps1 and install.sh use.
func skillsAddArgs(repo, tag, skill string, targets []string) []string {
	args := []string{"--yes", "skills", "add", skillSource(repo, tag, skill), "--global"}
	for _, target := range targets {
		args = append(args, "--agent", target)
	}
	return append(args, "-y")
}

// skillsRefresher reinstalls glorp's skills from a release (issue #797).
type skillsRefresher struct {
	Repo string
	// Run runs npx with args.
	Run func(ctx context.Context, args ...string) error
}

// Refresh installs every glorp skill at tag for targets, the skills.sh ids
// the agent registry lists. It stops at the first failure.
func (r skillsRefresher) Refresh(ctx context.Context, tag string, targets []string) error {
	if len(targets) == 0 {
		return errors.New("no agent lists a skills target")
	}
	for _, skill := range glorpSkills {
		if err := r.Run(ctx, skillsAddArgs(r.Repo, tag, skill, targets)...); err != nil {
			return fmt.Errorf("install %s: %w", skill, err)
		}
	}
	return nil
}

// npxCommand runs npx through the process package, or reports that it is not
// installed. Its stdin is left empty so skills.sh never waits on a prompt.
func npxCommand(ctx context.Context, args ...string) error {
	binary, err := exec.LookPath("npx")
	if err != nil {
		return fmt.Errorf("npx is not installed: %w", err)
	}
	output, err := process.CombinedOutput(exec.CommandContext(ctx, binary, args...))
	if err != nil {
		return fmt.Errorf("%w: %s", err, lastLine(string(output)))
	}
	return nil
}

// binarySkillsTargets reads the skills.sh targets from `binary agents
// -skills`, the list install.ps1 and install.sh build their --agent flags
// from.
func binarySkillsTargets(ctx context.Context, binary string) ([]string, error) {
	output, err := process.Output(exec.CommandContext(ctx, binary, "agents", "-skills"))
	if err != nil {
		return nil, fmt.Errorf("read the agent list from %s: %w", binary, err)
	}
	return strings.Fields(string(output)), nil
}

// releaseTag is the release tag a release version was built from.
func releaseTag(version string) string {
	return "v" + normalizedReleaseVersion(version)
}

// skillsCheck is the startup currency check (issue #797): it compares the
// installed skills with the ones the running release expects and reinstalls
// them when they are stale.
type skillsCheck struct {
	Version string
	Repo    string
	// Dir is where the installed copies are, and Bundle the expected ones.
	Dir     string
	Bundle  fs.FS
	Targets []string
	Refresh func(ctx context.Context, tag string, targets []string) error
	Logf    func(string, ...interface{})
}

// Run checks the installed skills once and refreshes stale ones. A
// development build has no release to install from and is not checked. It
// reports the skills still stale afterwards.
func (c skillsCheck) Run(ctx context.Context) []string {
	if _, ok := parseReleaseVersion(c.Version); !ok {
		return nil
	}
	stale := staleSkills(c.Bundle, c.Dir)
	if len(stale) == 0 {
		return nil
	}
	tag := releaseTag(c.Version)
	c.Logf("installed skills %s do not match glorp %s; reinstalling them", strings.Join(stale, ", "), tag)
	if err := c.Refresh(ctx, tag, c.Targets); err != nil {
		c.Logf("WARNING: could not reinstall skills %s for glorp %s, so agents run with stale instructions: %v. %s", strings.Join(stale, ", "), tag, err, skillsFixHint(c.Repo, tag))
		return stale
	}
	if stale = staleSkills(c.Bundle, c.Dir); len(stale) > 0 {
		c.Logf("WARNING: skills %s are still stale after reinstalling them for glorp %s. %s", strings.Join(stale, ", "), tag, skillsFixHint(c.Repo, tag))
		return stale
	}
	c.Logf("reinstalled skills for glorp %s", tag)
	return nil
}

func skillsFixHint(repo, tag string) string {
	return fmt.Sprintf("Reinstall them with: npx skills add %s --global --agent <agent> -y", skillSource(repo, tag, "<skill>"))
}

// skillsNote is what the agents report says about the installed skills: empty
// when they match version or version is a development build, and which are
// stale otherwise.
func skillsNote(version string, bundle fs.FS, dir string) string {
	if _, ok := parseReleaseVersion(version); !ok {
		return ""
	}
	stale := staleSkills(bundle, dir)
	if len(stale) == 0 {
		return ""
	}
	return fmt.Sprintf("%s stale for glorp %s; restart glorp to reinstall", strings.Join(stale, ", "), releaseTag(version))
}

// installedSkillsNote is skillsNote for the running binary and this user's
// installed skills.
func installedSkillsNote() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return skillsNote(version, bundledSkills, installedSkillsDir(home))
}
