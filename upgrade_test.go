package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// stubLatestTag returns a latestTag callback that always resolves to tag, or
// fails with err when err is non-nil.
func stubLatestTag(tag string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		return tag, err
	}
}

func TestUpgradeCommandUsesUnixInstaller(t *testing.T) {
	cmd := upgradeCommand(context.Background(), "darwin", "lsegal/glorp")
	want := []string{"bash", "-c", "set -o pipefail; curl -fsSL https://github.com/lsegal/glorp/releases/latest/download/install.sh | bash"}
	if got := cmd.Args; !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestUpgradeCommandUsesWindowsInstaller(t *testing.T) {
	cmd := upgradeCommand(context.Background(), "windows", "lsegal/glorp")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "powershell") || !strings.Contains(joined, "irm https://github.com/lsegal/glorp/releases/latest/download/install.ps1 | iex") {
		t.Fatalf("args = %q, want the PowerShell installer command", cmd.Args)
	}
}

func TestUpgradeRepoHonorsOverride(t *testing.T) {
	env := map[string]string{"GLORP_REPO": " someone/fork "}
	if got := upgradeRepo(func(key string) string { return env[key] }); got != "someone/fork" {
		t.Fatalf("upgradeRepo = %q, want someone/fork", got)
	}
	if got := upgradeRepo(func(string) string { return "" }); got != defaultUpgradeRepo {
		t.Fatalf("upgradeRepo = %q, want %q", got, defaultUpgradeRepo)
	}
	cmd := upgradeCommand(context.Background(), "linux", "someone/fork")
	if !strings.Contains(strings.Join(cmd.Args, " "), "https://github.com/someone/fork/releases/latest/download/install.sh") {
		t.Fatalf("args = %q, want the overridden repository URL", cmd.Args)
	}
}

func TestRunUpgradeStreamsInstallerOutput(t *testing.T) {
	var out bytes.Buffer
	err := runUpgrade(context.Background(), &out, "v1.0.0", stubLatestTag("v1.1.0", nil), func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "installed")
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Upgrading glorp with: echo installed", "installed", "glorp upgraded."} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want it to contain %q", got, want)
		}
	}
}

func TestRunUpgradeReportsInstallerFailure(t *testing.T) {
	var out bytes.Buffer
	err := runUpgrade(context.Background(), &out, "v1.0.0", stubLatestTag("v1.1.0", nil), func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	})
	if err == nil || !strings.Contains(err.Error(), "upgrade glorp") {
		t.Fatalf("err = %v, want an upgrade failure", err)
	}
	if strings.Contains(out.String(), "glorp upgraded.") {
		t.Fatalf("output = %q, want no success message", out.String())
	}
}

func TestRunUpgradeNoopsWhenAlreadyLatest(t *testing.T) {
	var out bytes.Buffer
	ranInstaller := false
	err := runUpgrade(context.Background(), &out, "v1.2.9", stubLatestTag("v1.2.9", nil), func(ctx context.Context) *exec.Cmd {
		ranInstaller = true
		return exec.CommandContext(ctx, "echo", "installed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if ranInstaller {
		t.Fatal("runUpgrade ran the installer even though the version already matches the latest release")
	}
	if got, want := out.String(), "glorp v1.2.9 is already the latest release.\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunUpgradeRunsInstallerWhenDevVersion(t *testing.T) {
	var out bytes.Buffer
	ranInstaller := false
	err := runUpgrade(context.Background(), &out, "dev", stubLatestTag("v1.2.9", nil), func(ctx context.Context) *exec.Cmd {
		ranInstaller = true
		return exec.CommandContext(ctx, "echo", "installed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ranInstaller {
		t.Fatal("runUpgrade should run the installer for an unversioned dev build")
	}
}

func TestRunUpgradeRunsInstallerWhenLatestReleaseLookupFails(t *testing.T) {
	var out bytes.Buffer
	ranInstaller := false
	err := runUpgrade(context.Background(), &out, "v1.2.9", stubLatestTag("", errors.New("network down")), func(ctx context.Context) *exec.Cmd {
		ranInstaller = true
		return exec.CommandContext(ctx, "echo", "installed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ranInstaller {
		t.Fatal("runUpgrade should fall back to running the installer when the release lookup fails")
	}
}

func TestLatestReleaseTagParsesTagName(t *testing.T) {
	doer := func(ctx context.Context, requestURL string) ([]byte, http.Header, int, error) {
		if want := latestReleaseURL("lsegal/glorp"); requestURL != want {
			t.Fatalf("requestURL = %q, want %q", requestURL, want)
		}
		return []byte(`{"tag_name": "v1.2.9"}`), nil, http.StatusOK, nil
	}
	got, err := latestReleaseTag(context.Background(), doer, "lsegal/glorp")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.2.9" {
		t.Fatalf("latestReleaseTag = %q, want %q", got, "v1.2.9")
	}
}

func TestLatestReleaseTagRejectsNonSuccessStatus(t *testing.T) {
	doer := func(ctx context.Context, requestURL string) ([]byte, http.Header, int, error) {
		return nil, nil, http.StatusNotFound, nil
	}
	if _, err := latestReleaseTag(context.Background(), doer, "lsegal/glorp"); err == nil {
		t.Fatal("latestReleaseTag = nil error, want an error for a non-2xx status")
	}
}
