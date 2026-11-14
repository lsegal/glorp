package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsegal/glorp/core"
	"github.com/lsegal/glorp/process"
	"github.com/lsegal/glorp/webui"
)

// defaultUpdateInterval is how often a watch checks for a newer glorp release
// unless --update-interval says otherwise (issue #783).
const defaultUpdateInterval = 5 * time.Minute

// minUpdateInterval is the shortest interval the updater accepts, so a typo
// cannot hit the releases API every few seconds. Zero turns the updater off.
const minUpdateInterval = time.Minute

// updateDownloadTimeout bounds one release download, so a stalled download
// cannot stop the updater for good.
const updateDownloadTimeout = 10 * time.Minute

// validateUpdateInterval rejects an interval the updater cannot run with.
func validateUpdateInterval(interval time.Duration) error {
	if interval != 0 && interval < minUpdateInterval {
		return fmt.Errorf("update interval must be 0 (off) or at least %s", formatInterval(minUpdateInterval))
	}
	return nil
}

// releaseArchiveName is the name of the release asset holding glorp for goos
// and goarch, as the installer scripts download it.
func releaseArchiveName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("glorp_%s_%s_%s%s", normalizedReleaseVersion(tag), goos, goarch, ext)
}

// releaseAssetURL is the direct download URL of a release asset, the one the
// installer scripts fetch.
func releaseAssetURL(repo, tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
}

// releaseBinaryName is the name of the glorp binary inside a release archive.
func releaseBinaryName(goos string) string {
	if goos == "windows" {
		return "glorp.exe"
	}
	return "glorp"
}

// parseReleaseVersion reads a version such as v1.5.22 into its numeric parts.
func parseReleaseVersion(version string) ([]int, bool) {
	version = normalizedReleaseVersion(version)
	if version == "" {
		return nil, false
	}
	var parts []int
	for _, field := range strings.Split(version, ".") {
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// newerRelease reports whether latest is a newer release than current. A
// development build, or a version that is not plain dotted numbers, is never
// updated, since there is no telling whether the release is newer.
func newerRelease(latest, current string) bool {
	have, ok := parseReleaseVersion(current)
	if !ok {
		return false
	}
	want, ok := parseReleaseVersion(latest)
	if !ok {
		return false
	}
	for i := 0; i < max(len(have), len(want)); i++ {
		var a, b int
		if i < len(have) {
			a = have[i]
		}
		if i < len(want) {
			b = want[i]
		}
		if a != b {
			return b > a
		}
	}
	return false
}

// updateStagingPrefix names the directories releaseDownloader downloads and
// unpacks each release into.
const updateStagingPrefix = "glorp-update-"

// removeUpdateStaging removes every release staging directory in dir. It is
// best effort: a directory that cannot be removed now is tried again later.
func removeUpdateStaging(dir string) {
	stale, _ := filepath.Glob(filepath.Join(dir, updateStagingPrefix+"*"))
	for _, path := range stale {
		_ = os.RemoveAll(path)
	}
}

// installUpdate swaps the staged binary in for target and then removes the
// staging directory it was unpacked into (issue #787). Failing to remove it
// does not fail the install; the next download tries again.
func installUpdate(target, staged string, windows bool) error {
	if err := replaceExecutable(target, staged, windows); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Dir(staged))
	return nil
}

// releaseDownloader fetches and unpacks a release's glorp binary for one
// platform.
type releaseDownloader struct {
	Repo   string
	GOOS   string
	GOARCH string
	// Dir is where archives are downloaded and binaries unpacked.
	Dir string
	// GH runs `gh` with args, or is nil when gh is not installed.
	GH func(ctx context.Context, args ...string) error
	// Fetch downloads url to the file dst without gh.
	Fetch func(ctx context.Context, url, dst string) error
}

// Download fetches tag's archive with `gh release download`, or straight from
// the release's download URL when gh is not installed or fails, and unpacks
// the glorp binary from it. It reports the unpacked binary's path.
func (d releaseDownloader) Download(ctx context.Context, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, updateDownloadTimeout)
	defer cancel()
	// Staging directories left by earlier downloads, such as an older
	// release this one replaces, are removed first (issue #787).
	removeUpdateStaging(d.Dir)
	dir := filepath.Join(d.Dir, updateStagingPrefix+normalizedReleaseVersion(tag))
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	asset := releaseArchiveName(tag, d.GOOS, d.GOARCH)
	archive := filepath.Join(dir, asset)
	var ghErr error
	if d.GH != nil {
		ghErr = d.GH(ctx, "release", "download", tag, "--repo", d.Repo, "--pattern", asset, "--dir", dir, "--clobber")
	}
	if d.GH == nil || ghErr != nil {
		if err := d.Fetch(ctx, releaseAssetURL(d.Repo, tag, asset), archive); err != nil {
			if ghErr != nil {
				return "", fmt.Errorf("download %s: gh: %v; direct: %w", asset, ghErr, err)
			}
			return "", fmt.Errorf("download %s: %w", asset, err)
		}
	}
	binary := filepath.Join(dir, releaseBinaryName(d.GOOS))
	if err := extractReleaseBinary(archive, releaseBinaryName(d.GOOS), binary); err != nil {
		return "", fmt.Errorf("unpack %s: %w", asset, err)
	}
	return binary, nil
}

// ghReleaseCommand runs gh with args through the process package, so a
// download still running at shutdown is reaped with everything else.
func ghReleaseCommand(binary string) func(context.Context, ...string) error {
	return func(ctx context.Context, args ...string) error {
		output, err := process.CombinedOutput(exec.CommandContext(ctx, binary, args...))
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
}

// latestReleaseTagVia looks up repo's latest release tag with gh when it is
// installed, which spends the authenticated rate limit rather than the
// 60-an-hour unauthenticated one glorp's other public lookups share, and
// falls back to the unauthenticated lookup `glorp upgrade` makes otherwise.
func latestReleaseTagVia(ctx context.Context, gh func(context.Context, ...string) ([]byte, error), doer publicAPIDoer, repo string) (string, error) {
	if gh != nil {
		output, err := gh(ctx, "api", "repos/"+repo+"/releases/latest", "--jq", ".tag_name")
		if tag := strings.TrimSpace(string(output)); err == nil && tag != "" {
			return tag, nil
		}
	}
	return latestReleaseTag(ctx, doer, repo)
}

// ghOutput runs gh with args through the process package and reports its
// standard output.
func ghOutput(binary string) func(context.Context, ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return process.Output(exec.CommandContext(ctx, binary, args...))
	}
}

// fetchReleaseAsset downloads url to dst over HTTPS, as the installer
// scripts do when they fetch the release archive.
func fetchReleaseAsset(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}
	file, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// extractReleaseBinary copies the file named name out of a release archive,
// a .zip or a .tar.gz, to dst.
func extractReleaseBinary(archive, name, dst string) error {
	if strings.HasSuffix(archive, ".zip") {
		reader, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer reader.Close()
		for _, file := range reader.File {
			if path.Base(file.Name) != name || file.FileInfo().IsDir() {
				continue
			}
			src, err := file.Open()
			if err != nil {
				return err
			}
			defer src.Close()
			return writeExecutable(dst, src)
		}
		return fmt.Errorf("%s not found in archive", name)
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == name {
			return writeExecutable(dst, reader)
		}
	}
}

func writeExecutable(dst string, src io.Reader) error {
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, src); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// replaceExecutable swaps the binary at target for staged while target may be
// running. On Windows a running image cannot be overwritten or deleted, but it
// can be renamed, so target is first moved aside to target.bak, as install.ps1
// does (issue #781). When target.bak is itself still running from an earlier
// update, target is moved to a fresh target.<hex>.bak instead. Elsewhere the
// new binary is renamed over target, which leaves the running process alone.
func replaceExecutable(target, staged string, windows bool) error {
	src, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".new-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if !windows {
		return os.Rename(tmpPath, target)
	}
	stale, _ := filepath.Glob(target + "*.bak")
	for _, backup := range stale {
		_ = os.Remove(backup)
	}
	backup := target + ".bak"
	if _, err := os.Lstat(backup); err == nil {
		suffix := make([]byte, 4)
		_, _ = rand.Read(suffix)
		backup = target + "." + hex.EncodeToString(suffix) + ".bak"
	}
	if err := renameRetry(target, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("move %s aside: %w", target, err)
	}
	if err := renameRetry(tmpPath, target); err != nil {
		_ = renameRetry(backup, target)
		return err
	}
	return nil
}

// renameRetryTimeout bounds how long renameRetry keeps trying.
const renameRetryTimeout = 5 * time.Second

// renameRetry renames from to to, retrying briefly while it fails. On Windows
// a file that was just written or renamed is often held open for a moment by
// antivirus or the search indexer, and renaming it then fails with a sharing
// violation (issue #799). A missing source fails at once.
func renameRetry(from, to string) error {
	deadline := time.Now().Add(renameRetryTimeout)
	delay := 10 * time.Millisecond
	for {
		err := os.Rename(from, to)
		if err == nil || errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
}

// updater keeps a watch on the newest glorp release (issue #783). Each round
// it looks up the latest release and, when it is newer than the running
// version, downloads it and tells the dashboards an update is available. When
// no agent is running it applies the update on its own: the new binary is
// swapped in and glorp restarts through the restart button's path, which
// pushes every run's work first. Otherwise the dashboards' update buttons
// apply it.
type updater struct {
	Current  string
	Interval time.Duration
	// LatestTag looks up the newest release's tag.
	LatestTag func(context.Context) (string, error)
	// Download fetches a release and reports its unpacked binary's path.
	Download func(ctx context.Context, tag string) (string, error)
	// Install swaps the downloaded binary in for the running one.
	Install func(staged string) error
	// RefreshSkills reinstalls the gh-fix, gh-discuss, and gh-create skills
	// for the release tag Install swapped in (issue #797), so binary and
	// skills match after an update as they do after the installers run.
	RefreshSkills func(ctx context.Context, tag string) error
	// Restart restarts glorp onto the binary Install swapped in, reporting
	// each run's shutdown checkpoint.
	Restart func(context.Context) ([]core.CheckpointResult, error)
	// Active reports how many agent runs are in flight.
	Active func() int
	// Notify tells the dashboards an update to version is available.
	Notify func(version string)
	Logf   func(string, ...interface{})

	mu        sync.Mutex
	available string
	staged    string
	// installed is the tag Install last swapped in, so a retry after a
	// failed restart does not install again from a removed staging
	// directory.
	installed string
	applying  atomic.Bool
}

// Run checks for an update right away and then every Interval until ctx ends.
func (u *updater) Run(ctx context.Context) {
	ticker := time.NewTicker(u.Interval)
	defer ticker.Stop()
	for {
		u.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// check runs one round. A failed lookup or download is silent and simply
// tried again next round.
func (u *updater) check(ctx context.Context) {
	if u.applying.Load() {
		return
	}
	tag, err := u.LatestTag(ctx)
	if err != nil || !newerRelease(tag, u.Current) {
		return
	}
	u.mu.Lock()
	known := u.available == tag
	u.mu.Unlock()
	if !known {
		staged, err := u.Download(ctx, tag)
		if err != nil {
			return
		}
		u.mu.Lock()
		u.available, u.staged = tag, staged
		u.mu.Unlock()
		u.Logf("glorp %s is available (running %s)", tag, u.Current)
		u.Notify(tag)
	}
	if active := u.Active(); active > 0 {
		return
	}
	u.Logf("no agents are active; updating glorp to %s", tag)
	if _, err := u.Apply(ctx); err != nil {
		u.Logf("update glorp to %s: %v", tag, err)
	}
}

// Available is the release tag a downloaded update would install, or "" when
// glorp is current.
func (u *updater) Available() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.available
}

// Apply swaps in the downloaded update and restarts glorp on it. It backs the
// dashboards' update buttons as well as the automatic update, and reports
// each run's shutdown checkpoint as the restart button does.
func (u *updater) Apply(ctx context.Context) ([]core.CheckpointResult, error) {
	u.mu.Lock()
	tag, staged, installed := u.available, u.staged, u.installed
	u.mu.Unlock()
	if tag == "" {
		return nil, fmt.Errorf("no glorp update is available")
	}
	if !u.applying.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("glorp is already updating")
	}
	if installed != tag {
		if err := u.Install(staged); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// The staged binary is gone, such as removed by
				// another glorp's cleanup, so the next round
				// downloads the release again.
				u.mu.Lock()
				if u.available == tag {
					u.available, u.staged = "", ""
				}
				u.mu.Unlock()
			}
			u.applying.Store(false)
			return nil, fmt.Errorf("install glorp %s: %w", tag, err)
		}
		u.mu.Lock()
		u.installed = tag
		u.mu.Unlock()
		// A failed refresh does not hold the update back: the binary is
		// already swapped in, and the restarted glorp's startup check
		// tries the skills again and reports them if it cannot.
		if u.RefreshSkills != nil {
			if err := u.RefreshSkills(ctx, tag); err != nil {
				u.Logf("WARNING: could not update skills for glorp %s: %v", tag, err)
			} else {
				u.Logf("updated skills for glorp %s", tag)
			}
		}
		u.Logf("installed glorp %s; restarting", tag)
	}
	results, err := u.Restart(ctx)
	if err != nil {
		u.applying.Store(false)
		return nil, err
	}
	return results, nil
}

// newWatchUpdater builds the updater a watch runs, or nil when it is turned
// off or glorp is a development build with no release version to compare.
func newWatchUpdater(interval time.Duration, active *activeAgents, restart func(context.Context) ([]core.CheckpointResult, error), logf func(string, ...interface{}), ui *TerminalUI, web *webui.Server) *updater {
	if interval == 0 {
		return nil
	}
	if _, ok := parseReleaseVersion(version); !ok {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		logf("glorp updates are off: %v", err)
		return nil
	}
	target := restartExecutable(executable)
	repo := upgradeRepo(os.Getenv)
	return &updater{
		Current:  version,
		Interval: interval,
		LatestTag: func(ctx context.Context) (string, error) {
			var gh func(context.Context, ...string) ([]byte, error)
			if binary, err := exec.LookPath("gh"); err == nil {
				gh = ghOutput(binary)
			}
			return latestReleaseTagVia(ctx, gh, doPublicGitHubRequest, repo)
		},
		Download: func(ctx context.Context, tag string) (string, error) {
			downloader := releaseDownloader{Repo: repo, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Dir: os.TempDir(), Fetch: fetchReleaseAsset}
			// gh is looked up each time, so installing it while glorp runs
			// is picked up by the next download.
			if binary, err := exec.LookPath("gh"); err == nil {
				downloader.GH = ghReleaseCommand(binary)
			}
			return downloader.Download(ctx, tag)
		},
		Install: func(staged string) error {
			return installUpdate(target, staged, runtime.GOOS == "windows")
		},
		RefreshSkills: func(ctx context.Context, tag string) error {
			// The agent list comes from the binary just swapped in, as the
			// installers read it, so a new release's agents are included.
			targets, err := binarySkillsTargets(ctx, target)
			if err != nil {
				return err
			}
			return skillsRefresher{Repo: repo, Run: npxCommand}.Refresh(ctx, tag, targets)
		},
		Restart: restart,
		Active:  active.count,
		Notify: func(version string) {
			if ui != nil {
				ui.SetUpdate(version)
			}
			if web != nil {
				web.SetUpdate(version)
			}
		},
		Logf: logf,
	}
}
