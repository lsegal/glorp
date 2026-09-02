package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/lsegal/glorp/process"
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

// latestReleaseURL returns the well-known API URL for a repository's newest
// published release.
func latestReleaseURL(repo string) string {
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
}

// latestReleaseTag returns the tag name of repo's latest published release,
// using doer to perform the HTTP GET so tests can stub the network.
func latestReleaseTag(ctx context.Context, doer publicAPIDoer, repo string) (string, error) {
	body, _, status, err := doer(ctx, latestReleaseURL(repo))
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("fetch latest release: unexpected status %d", status)
	}
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	if data.TagName == "" {
		return "", fmt.Errorf("fetch latest release: response had no tag_name")
	}
	return data.TagName, nil
}

func normalizedReleaseVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// runUpgrade runs the platform installer, streaming its output to out, unless
// currentVersion already matches the latest published release, in which case
// it noops and reports the version the user is on instead of downloading
// anything. A release-lookup failure is not fatal: it just falls back to
// running the installer as if the check had not been made.
func runUpgrade(ctx context.Context, out io.Writer, currentVersion string, latestTag func(context.Context) (string, error), newCommand func(context.Context) *exec.Cmd) error {
	if currentVersion != "" && currentVersion != "dev" {
		if latest, err := latestTag(ctx); err == nil && normalizedReleaseVersion(latest) == normalizedReleaseVersion(currentVersion) {
			fmt.Fprintf(out, "glorp %s is already the latest release.\n", currentVersion)
			return nil
		}
	}
	cmd := newCommand(ctx)
	fmt.Fprintf(out, "Upgrading glorp with: %s\n", strings.Join(cmd.Args, " "))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	if err := process.Run(cmd); err != nil {
		return fmt.Errorf("upgrade glorp: %w", err)
	}
	fmt.Fprintln(out, "glorp upgraded.")
	return nil
}
