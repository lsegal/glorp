package main

import (
	"bytes"
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

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
	err := runUpgrade(context.Background(), &out, func(ctx context.Context) *exec.Cmd {
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
	err := runUpgrade(context.Background(), &out, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	})
	if err == nil || !strings.Contains(err.Error(), "upgrade glorp") {
		t.Fatalf("err = %v, want an upgrade failure", err)
	}
	if strings.Contains(out.String(), "glorp upgraded.") {
		t.Fatalf("output = %q, want no success message", out.String())
	}
}
