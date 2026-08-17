package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// defaultUpgradeRepo is the repository whose published installers upgrade glorp.
const defaultUpgradeRepo = "lsegal/glorp"

// upgradeRepo returns the repository the installer should be fetched from,
// honoring the same GLORP_REPO override the installers themselves support.
func upgradeRepo(lookup func(string) string) string {
	if repo := strings.TrimSpace(lookup("GLORP_REPO")); repo != "" {
		return repo
	}
	return defaultUpgradeRepo
}

// upgradeInstallerURL returns the well-known download URL for an installer script.
func upgradeInstallerURL(repo, script string) string {
	return fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", repo, script)
}

// upgradeCommand builds the documented install command for the given platform,
// so upgrading runs exactly what a fresh installation runs.
func upgradeCommand(ctx context.Context, goos, repo string) *exec.Cmd {
	if goos == "windows" {
		url := upgradeInstallerURL(repo, "install.ps1")
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", fmt.Sprintf("$ErrorActionPreference = 'Stop'; irm %s | iex", url))
	}
	url := upgradeInstallerURL(repo, "install.sh")
	return exec.CommandContext(ctx, "bash", "-c", fmt.Sprintf("set -o pipefail; curl -fsSL %s | bash", url))
}

// runUpgrade runs the platform installer, streaming its output to out.
func runUpgrade(ctx context.Context, out io.Writer, newCommand func(context.Context) *exec.Cmd) error {
	cmd := newCommand(ctx)
	fmt.Fprintf(out, "Upgrading glorp with: %s\n", strings.Join(cmd.Args, " "))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	if err := runChildProcess(cmd); err != nil {
		return fmt.Errorf("upgrade glorp: %w", err)
	}
	fmt.Fprintln(out, "glorp upgraded.")
	return nil
}
