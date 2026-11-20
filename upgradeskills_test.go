package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRunUpgradeSkillsInstallsTheRunningReleaseSkills(t *testing.T) {
	var gotTag string
	var gotTargets []string
	var out bytes.Buffer
	err := runUpgradeSkills(context.Background(), &out, "1.6.10", []string{"codex", "claude-code"}, func(context.Context) (string, error) {
		t.Fatal("a release build must not look up the latest release")
		return "", nil
	}, func(_ context.Context, tag string, targets []string) error {
		gotTag, gotTargets = tag, targets
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotTag != "v1.6.10" {
		t.Fatalf("tag = %q, want v1.6.10", gotTag)
	}
	if !reflect.DeepEqual(gotTargets, []string{"codex", "claude-code"}) {
		t.Fatalf("targets = %v", gotTargets)
	}
	for _, want := range append([]string{"Reinstalled skills", "v1.6.10", "codex, claude-code"}, glorpSkills...) {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want it to mention %q", out.String(), want)
		}
	}
}

func TestRunUpgradeSkillsDevBuildUsesLatestRelease(t *testing.T) {
	var gotTag string
	err := runUpgradeSkills(context.Background(), &bytes.Buffer{}, "dev", []string{"codex"}, func(context.Context) (string, error) {
		return "v1.7.0", nil
	}, func(_ context.Context, tag string, _ []string) error {
		gotTag = tag
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotTag != "v1.7.0" {
		t.Fatalf("tag = %q, want v1.7.0", gotTag)
	}
}

func TestRunUpgradeSkillsReportsFailures(t *testing.T) {
	var out bytes.Buffer
	err := runUpgradeSkills(context.Background(), &out, "1.6.10", []string{"codex"}, nil, func(context.Context, string, []string) error {
		return errors.New("install gh-fix: npx is not installed")
	})
	if err == nil || !strings.Contains(err.Error(), "npx is not installed") {
		t.Fatalf("err = %v, want the refresh failure", err)
	}
	if strings.Contains(out.String(), "Reinstalled skills") {
		t.Fatalf("output = %q, must not claim success", out.String())
	}
	err = runUpgradeSkills(context.Background(), &bytes.Buffer{}, "dev", []string{"codex"}, func(context.Context) (string, error) {
		return "", errors.New("offline")
	}, func(context.Context, string, []string) error {
		t.Fatal("must not refresh without a tag")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err = %v, want the release lookup failure", err)
	}
}

func TestUpgradeSkillsCommandTakesConfigFlag(t *testing.T) {
	flags := commandFlags("upgrade-skills")
	if flags == nil || flags.Lookup("config") == nil {
		t.Fatal("upgrade-skills has no -config flag")
	}
}
