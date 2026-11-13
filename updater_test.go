package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewerRelease(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.5.23", "v1.5.22", true},
		{"v1.5.23", "1.5.22", true},
		{"v1.6.0", "v1.5.22", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.5.10", "v1.5.9", true},
		{"v1.5.22", "v1.5.22", false},
		{"v1.5.21", "v1.5.22", false},
		{"v1.5", "v1.5.0", false},
		{"v1.5.23", "dev", false},
		{"v1.5.23", "", false},
		{"nightly", "v1.5.22", false},
	} {
		if got := newerRelease(tc.latest, tc.current); got != tc.want {
			t.Errorf("newerRelease(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

// The release archive names match what install.sh and install.ps1 download.
func TestReleaseArchiveName(t *testing.T) {
	if got := releaseArchiveName("v1.5.23", "linux", "amd64"); got != "glorp_1.5.23_linux_amd64.tar.gz" {
		t.Errorf("linux archive = %q", got)
	}
	if got := releaseArchiveName("v1.5.23", "darwin", "arm64"); got != "glorp_1.5.23_darwin_arm64.tar.gz" {
		t.Errorf("darwin archive = %q", got)
	}
	if got := releaseArchiveName("v1.5.23", "windows", "amd64"); got != "glorp_1.5.23_windows_amd64.zip" {
		t.Errorf("windows archive = %q", got)
	}
	if got := releaseAssetURL("lsegal/glorp", "v1.5.23", "glorp_1.5.23_windows_amd64.zip"); got != "https://github.com/lsegal/glorp/releases/download/v1.5.23/glorp_1.5.23_windows_amd64.zip" {
		t.Errorf("asset URL = %q", got)
	}
}

func TestValidateUpdateInterval(t *testing.T) {
	if err := validateUpdateInterval(0); err != nil {
		t.Errorf("0 rejected: %v", err)
	}
	if err := validateUpdateInterval(5 * time.Minute); err != nil {
		t.Errorf("5m rejected: %v", err)
	}
	if err := validateUpdateInterval(time.Second); err == nil {
		t.Error("1s accepted")
	}
}

// fakeUpdater records what an updater round did.
type fakeUpdater struct {
	latest      string
	latestErr   error
	active      int
	downloads   []string
	downloadErr error
	installs    []string
	restarts    int
	notices     []string
	logs        []string
}

func (f *fakeUpdater) updater() *updater {
	return &updater{
		Current:  "v1.5.22",
		Interval: time.Minute,
		LatestTag: func(context.Context) (string, error) {
			return f.latest, f.latestErr
		},
		Download: func(_ context.Context, tag string) (string, error) {
			f.downloads = append(f.downloads, tag)
			if f.downloadErr != nil {
				return "", f.downloadErr
			}
			return "/staged/" + tag, nil
		},
		Install: func(staged string) error {
			f.installs = append(f.installs, staged)
			return nil
		},
		Restart: func(context.Context) error {
			f.restarts++
			return nil
		},
		Active: func() int { return f.active },
		Notify: func(version string) { f.notices = append(f.notices, version) },
		Logf: func(format string, args ...interface{}) {
			f.logs = append(f.logs, fmt.Sprintf(format, args...))
		},
	}
}

func TestUpdaterDoesNothingWhenCurrent(t *testing.T) {
	f := &fakeUpdater{latest: "v1.5.22"}
	u := f.updater()
	u.check(context.Background())
	if len(f.downloads) != 0 || len(f.notices) != 0 || len(f.installs) != 0 || len(f.logs) != 0 {
		t.Fatalf("current glorp: downloads=%v notices=%v installs=%v logs=%v", f.downloads, f.notices, f.installs, f.logs)
	}
	if u.Available() != "" {
		t.Fatalf("Available = %q", u.Available())
	}
}

// A failed lookup or download is silent and retried on the next round.
func TestUpdaterFailuresAreSilentAndRetried(t *testing.T) {
	f := &fakeUpdater{latestErr: errors.New("offline")}
	u := f.updater()
	u.check(context.Background())
	if len(f.downloads) != 0 || len(f.logs) != 0 {
		t.Fatalf("failed lookup: downloads=%v logs=%v", f.downloads, f.logs)
	}
	f.latestErr, f.latest, f.downloadErr = nil, "v1.5.23", errors.New("reset by peer")
	f.active = 1
	u.check(context.Background())
	if len(f.notices) != 0 || len(f.logs) != 0 || u.Available() != "" {
		t.Fatalf("failed download: notices=%v logs=%v available=%q", f.notices, f.logs, u.Available())
	}
	f.downloadErr = nil
	u.check(context.Background())
	if len(f.downloads) != 2 || len(f.notices) != 1 || u.Available() != "v1.5.23" {
		t.Fatalf("retry: downloads=%v notices=%v available=%q", f.downloads, f.notices, u.Available())
	}
}

func TestUpdaterAppliesWhenNoAgentsAreActive(t *testing.T) {
	f := &fakeUpdater{latest: "v1.5.23"}
	u := f.updater()
	u.check(context.Background())
	if len(f.downloads) != 1 || len(f.notices) != 1 || f.notices[0] != "v1.5.23" {
		t.Fatalf("downloads=%v notices=%v", f.downloads, f.notices)
	}
	if len(f.installs) != 1 || f.installs[0] != "/staged/v1.5.23" || f.restarts != 1 {
		t.Fatalf("installs=%v restarts=%d", f.installs, f.restarts)
	}
	// A round while the restart is under way does nothing more.
	u.check(context.Background())
	if len(f.downloads) != 1 || len(f.installs) != 1 || f.restarts != 1 {
		t.Fatalf("second round: downloads=%v installs=%v restarts=%d", f.downloads, f.installs, f.restarts)
	}
}

// With an agent running the update is only offered, and it applies itself on
// the first round after the last agent finishes, without downloading again.
func TestUpdaterWaitsForActiveAgents(t *testing.T) {
	f := &fakeUpdater{latest: "v1.5.23", active: 1}
	u := f.updater()
	u.check(context.Background())
	u.check(context.Background())
	if len(f.notices) != 1 || u.Available() != "v1.5.23" || len(f.installs) != 0 || f.restarts != 0 {
		t.Fatalf("active agent: notices=%v available=%q installs=%v restarts=%d", f.notices, u.Available(), f.installs, f.restarts)
	}
	f.active = 0
	u.check(context.Background())
	if len(f.downloads) != 1 || len(f.installs) != 1 || f.restarts != 1 {
		t.Fatalf("idle: downloads=%v installs=%v restarts=%d", f.downloads, f.installs, f.restarts)
	}
}

// The dashboards' buttons apply the update while agents are running.
func TestUpdaterApplyFromButton(t *testing.T) {
	f := &fakeUpdater{latest: "v1.5.23", active: 2}
	u := f.updater()
	if err := u.Apply(context.Background()); err == nil {
		t.Fatal("Apply with no update available succeeded")
	}
	u.check(context.Background())
	if err := u.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.installs) != 1 || f.restarts != 1 {
		t.Fatalf("installs=%v restarts=%d", f.installs, f.restarts)
	}
	if err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "already updating") {
		t.Fatalf("second Apply = %v, want already updating", err)
	}
}

// An install that fails leaves the button usable again.
func TestUpdaterApplyInstallFailure(t *testing.T) {
	f := &fakeUpdater{latest: "v1.5.23", active: 1}
	u := f.updater()
	u.check(context.Background())
	failures := 1
	u.Install = func(string) error {
		if failures > 0 {
			failures--
			return errors.New("access denied")
		}
		return nil
	}
	if err := u.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("Apply = %v, want access denied", err)
	}
	if f.restarts != 0 {
		t.Fatalf("restarted after a failed install")
	}
	if err := u.Apply(context.Background()); err != nil || f.restarts != 1 {
		t.Fatalf("retry Apply = %v, restarts=%d", err, f.restarts)
	}
}

func tarGzArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, file := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0o755, Size: int64(len(file.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, content}} {
		w, err := zw.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// gh downloads the release when it is installed.
func TestReleaseDownloaderUsesGH(t *testing.T) {
	archive := tarGzArchive(t, "glorp", []byte("new glorp"))
	var ghArgs []string
	fetched := false
	d := releaseDownloader{Repo: "o/glorp", GOOS: "linux", GOARCH: "amd64", Dir: t.TempDir(),
		GH: func(_ context.Context, args ...string) error {
			ghArgs = args
			dir := args[len(args)-2]
			return os.WriteFile(filepath.Join(dir, "glorp_1.5.23_linux_amd64.tar.gz"), archive, 0o644)
		},
		Fetch: func(context.Context, string, string) error { fetched = true; return nil },
	}
	binary, err := d.Download(context.Background(), "v1.5.23")
	if err != nil {
		t.Fatal(err)
	}
	want := "release download v1.5.23 --repo o/glorp --pattern glorp_1.5.23_linux_amd64.tar.gz --dir"
	if got := strings.Join(ghArgs, " "); !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "--clobber") {
		t.Fatalf("gh args = %q, want prefix %q", got, want)
	}
	if fetched {
		t.Fatal("direct download used although gh succeeded")
	}
	if got, _ := os.ReadFile(binary); string(got) != "new glorp" || filepath.Base(binary) != "glorp" {
		t.Fatalf("binary %s = %q", binary, got)
	}
}

// Without gh, or when gh fails, the release asset is downloaded directly.
func TestReleaseDownloaderFallsBackWithoutGH(t *testing.T) {
	archive := zipArchive(t, "glorp.exe", []byte("new glorp.exe"))
	for _, tc := range []struct {
		name string
		gh   func(context.Context, ...string) error
	}{
		{"no gh", nil},
		{"gh fails", func(context.Context, ...string) error { return errors.New("gh: not logged in") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fetchedURL string
			d := releaseDownloader{Repo: "o/glorp", GOOS: "windows", GOARCH: "arm64", Dir: t.TempDir(), GH: tc.gh,
				Fetch: func(_ context.Context, url, dst string) error {
					fetchedURL = url
					return os.WriteFile(dst, archive, 0o644)
				},
			}
			binary, err := d.Download(context.Background(), "v1.5.23")
			if err != nil {
				t.Fatal(err)
			}
			if fetchedURL != "https://github.com/o/glorp/releases/download/v1.5.23/glorp_1.5.23_windows_arm64.zip" {
				t.Fatalf("fetched %q", fetchedURL)
			}
			if got, _ := os.ReadFile(binary); string(got) != "new glorp.exe" || filepath.Base(binary) != "glorp.exe" {
				t.Fatalf("binary %s = %q", binary, got)
			}
		})
	}
}

func TestReleaseDownloaderReportsBothFailures(t *testing.T) {
	d := releaseDownloader{Repo: "o/glorp", GOOS: "linux", GOARCH: "amd64", Dir: t.TempDir(),
		GH:    func(context.Context, ...string) error { return errors.New("gh broke") },
		Fetch: func(context.Context, string, string) error { return errors.New("404") },
	}
	if _, err := d.Download(context.Background(), "v1.5.23"); err == nil || !strings.Contains(err.Error(), "gh broke") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("Download = %v", err)
	}
}

// TestUpdaterHelperProcess is not a real test: replaceExecutable's test runs a
// copy of the test binary as a stand-in for a running glorp.
func TestUpdaterHelperProcess(t *testing.T) {
	if os.Getenv("GLORP_UPDATER_HELPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

// The running binary is replaced in place while it runs. On Windows it is
// renamed to .bak first (issue #781), and a second update while the first
// image still runs from .bak uses a fresh backup name.
func TestReplaceExecutableWhileRunning(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, releaseBinaryName(runtime.GOOS))
	if err := os.WriteFile(target, original, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(target, "-test.run=^TestUpdaterHelperProcess$")
	cmd.Env = append(os.Environ(), "GLORP_UPDATER_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	windows := runtime.GOOS == "windows"
	for i, content := range []string{"glorp v2", "glorp v3"} {
		staged := filepath.Join(t.TempDir(), "staged")
		if err := os.WriteFile(staged, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := replaceExecutable(target, staged, windows); err != nil {
			t.Fatalf("update %d: %v", i+1, err)
		}
		if got, _ := os.ReadFile(target); string(got) != content {
			t.Fatalf("update %d: target = %q, want %q", i+1, got, content)
		}
		select {
		case err := <-exited:
			t.Fatalf("update %d: running glorp exited: %v", i+1, err)
		default:
		}
	}
	if info, err := os.Stat(target); err != nil || (!windows && info.Mode().Perm()&0o100 == 0) {
		t.Fatalf("target not executable: %v %v", info, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !windows {
		if len(names) != 1 {
			t.Fatalf("files left beside glorp: %v", names)
		}
		return
	}
	// The original image still runs from glorp.exe.bak, so the second update
	// moved v2 to a fresh glorp.exe.<hex>.bak.
	backup, err := os.ReadFile(target + ".bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("glorp.exe.bak is not the running original: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("files = %v, want glorp.exe, glorp.exe.bak, and one fresh backup", names)
	}
	for _, name := range names {
		if name == "glorp.exe" || name == "glorp.exe.bak" {
			continue
		}
		if got, _ := os.ReadFile(filepath.Join(dir, name)); !strings.HasSuffix(name, ".bak") || string(got) != "glorp v2" {
			t.Fatalf("extra file %s = %q, want the v2 backup", name, got)
		}
	}
}

// The background lookup prefers gh's authenticated API and falls back to the
// public one when gh is missing or fails.
func TestLatestReleaseTagVia(t *testing.T) {
	publicCalls := 0
	public := func(_ context.Context, url string) ([]byte, http.Header, int, error) {
		publicCalls++
		if url != "https://api.github.com/repos/o/glorp/releases/latest" {
			t.Errorf("public URL = %q", url)
		}
		return []byte(`{"tag_name":"v1.5.23"}`), nil, 200, nil
	}
	var ghArgs []string
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		ghArgs = args
		return []byte("v1.5.24\n"), nil
	}
	if tag, err := latestReleaseTagVia(context.Background(), gh, public, "o/glorp"); err != nil || tag != "v1.5.24" || publicCalls != 0 {
		t.Fatalf("with gh: tag=%q err=%v public calls=%d", tag, err, publicCalls)
	}
	if got := strings.Join(ghArgs, " "); got != "api repos/o/glorp/releases/latest --jq .tag_name" {
		t.Fatalf("gh args = %q", got)
	}
	failing := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("HTTP 401") }
	if tag, err := latestReleaseTagVia(context.Background(), failing, public, "o/glorp"); err != nil || tag != "v1.5.23" || publicCalls != 1 {
		t.Fatalf("gh failing: tag=%q err=%v public calls=%d", tag, err, publicCalls)
	}
	if tag, err := latestReleaseTagVia(context.Background(), nil, public, "o/glorp"); err != nil || tag != "v1.5.23" || publicCalls != 2 {
		t.Fatalf("no gh: tag=%q err=%v public calls=%d", tag, err, publicCalls)
	}
}
