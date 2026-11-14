package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// installSkills copies the bundled skills into dir the way skills.sh lays
// them out, optionally with CRLF line endings.
func installSkills(t *testing.T, dir string, crlf bool) {
	t.Helper()
	if err := copySkills(dir, crlf); err != nil {
		t.Fatal(err)
	}
}

func copySkills(dir string, crlf bool) error {
	return fs.WalkDir(bundledSkills, bundledSkillsRoot, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(bundledSkills, file)
		if err != nil {
			return err
		}
		// A Windows checkout may already embed CRLF, so the copy starts
		// from LF either way.
		data = normalizeNewlines(data)
		if crlf {
			data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
		}
		dst := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(file, bundledSkillsRoot+"/")))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

func TestStaleSkills(t *testing.T) {
	dir := t.TempDir()
	if got := staleSkills(bundledSkills, dir); !reflect.DeepEqual(got, glorpSkills) {
		t.Fatalf("nothing installed: stale = %v, want %v", got, glorpSkills)
	}
	installSkills(t, dir, false)
	if got := staleSkills(bundledSkills, dir); len(got) != 0 {
		t.Fatalf("current skills: stale = %v", got)
	}
	installSkills(t, dir, true)
	if got := staleSkills(bundledSkills, dir); len(got) != 0 {
		t.Fatalf("CRLF skills: stale = %v", got)
	}
	// A copy that predates a skill change, such as gh-create before its
	// creation marker, is stale.
	if err := os.WriteFile(filepath.Join(dir, "gh-create", "SKILL.md"), []byte("---\nname: gh-create\n---\nold text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "gh-fix", "agents", "openai.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := staleSkills(bundledSkills, dir); !reflect.DeepEqual(got, []string{"gh-fix", "gh-create"}) {
		t.Fatalf("stale = %v, want gh-fix and gh-create", got)
	}
}

// The refresh installs each skill pinned to the release tag, for every agent
// target, in the shape the installers use.
func TestSkillsRefresherInstallsEverySkillAtTheTag(t *testing.T) {
	var calls [][]string
	refresher := skillsRefresher{Repo: "o/r", Run: func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}}
	if err := refresher.Refresh(context.Background(), "v1.5.26", []string{"claude-code", "codex"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{}
	for _, skill := range glorpSkills {
		want = append(want, []string{"--yes", "skills", "add", "https://github.com/o/r/tree/v1.5.26/.agents/skills/" + skill, "--global", "--agent", "claude-code", "--agent", "codex", "-y"})
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("npx calls = %q, want %q", calls, want)
	}
	if err := refresher.Refresh(context.Background(), "v1.5.26", nil); err == nil {
		t.Fatal("Refresh with no targets succeeded")
	}
	failing := skillsRefresher{Repo: "o/r", Run: func(context.Context, ...string) error { return errors.New("npx is not installed") }}
	if err := failing.Refresh(context.Background(), "v1.5.26", []string{"codex"}); err == nil || !strings.Contains(err.Error(), "gh-fix") {
		t.Fatalf("failing Refresh = %v, want the skill named", err)
	}
}

type fakeSkillsCheck struct {
	dir       string
	refreshes []string
	targets   []string
	err       error
	logs      []string
}

func (f *fakeSkillsCheck) check(version string) skillsCheck {
	return skillsCheck{
		Version: version,
		Repo:    "o/r",
		Dir:     f.dir,
		Bundle:  bundledSkills,
		Targets: []string{"claude-code"},
		Refresh: func(_ context.Context, tag string, targets []string) error {
			f.refreshes = append(f.refreshes, tag)
			f.targets = targets
			if f.err != nil {
				return f.err
			}
			return copySkills(f.dir, false)
		},
		Logf: func(format string, args ...interface{}) { f.logs = append(f.logs, fmt.Sprintf(format, args...)) },
	}
}

// Stale skills found at startup are reinstalled for the running release.
func TestSkillsCheckRefreshesStaleSkills(t *testing.T) {
	f := &fakeSkillsCheck{dir: t.TempDir()}
	if stale := f.check("1.5.26").Run(context.Background()); len(stale) != 0 {
		t.Fatalf("still stale = %v", stale)
	}
	if !reflect.DeepEqual(f.refreshes, []string{"v1.5.26"}) || !reflect.DeepEqual(f.targets, []string{"claude-code"}) {
		t.Fatalf("refreshes = %v targets = %v", f.refreshes, f.targets)
	}
	if got := staleSkills(bundledSkills, f.dir); len(got) != 0 {
		t.Fatalf("after refresh: stale = %v", got)
	}
	// Once current, a later start does nothing.
	f.logs = nil
	f.check("1.5.26").Run(context.Background())
	if len(f.refreshes) != 1 || len(f.logs) != 0 {
		t.Fatalf("current skills: refreshes = %v logs = %v", f.refreshes, f.logs)
	}
}

// Skills that cannot be reinstalled are reported loudly, not left silent.
func TestSkillsCheckReportsSkillsItCannotRefresh(t *testing.T) {
	f := &fakeSkillsCheck{dir: t.TempDir(), err: errors.New("npx is not installed")}
	installSkills(t, f.dir, false)
	if err := os.WriteFile(filepath.Join(f.dir, "gh-fix", "SKILL.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := f.check("v1.5.26").Run(context.Background())
	if !reflect.DeepEqual(stale, []string{"gh-fix"}) {
		t.Fatalf("stale = %v, want gh-fix", stale)
	}
	last := f.logs[len(f.logs)-1]
	if !strings.Contains(last, "WARNING") || !strings.Contains(last, "gh-fix") || !strings.Contains(last, "npx is not installed") {
		t.Fatalf("log = %q, want a warning naming the skill and the reason", last)
	}
	if note := skillsNote("v1.5.26", bundledSkills, f.dir); !strings.Contains(note, "gh-fix") || strings.Contains(note, "gh-create") {
		t.Fatalf("skillsNote = %q, want only gh-fix", note)
	}
}

// A development build has no release to install from and is not checked.
func TestSkillsCheckSkipsDevelopmentBuilds(t *testing.T) {
	f := &fakeSkillsCheck{dir: t.TempDir()}
	if stale := f.check("dev").Run(context.Background()); stale != nil || len(f.refreshes) != 0 || len(f.logs) != 0 {
		t.Fatalf("dev build: stale = %v refreshes = %v logs = %v", stale, f.refreshes, f.logs)
	}
	if note := skillsNote("dev", bundledSkills, f.dir); note != "" {
		t.Fatalf("dev skillsNote = %q", note)
	}
}
