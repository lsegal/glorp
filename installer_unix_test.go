//go:build !windows

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeInstallGH stands in for gh. For `gh api` it logs its arguments to
// GLORP_TEST_LOOKUP_LOG and fails, so a test sees any API lookup the script
// still makes. Otherwise it logs them to GLORP_TEST_LOG, then either copies
// the test archive to --dir/--pattern the way `gh release download` would or
// fails, as GLORP_TEST_GH says.
const fakeInstallGH = `#!/bin/sh
if [ "$1" = api ]; then
  echo "gh $*" >> "$GLORP_TEST_LOOKUP_LOG"
  echo "gh: lookup failed" >&2
  exit 1
fi
echo "gh $*" >> "$GLORP_TEST_LOG"
[ "$GLORP_TEST_GH" = ok ] || { echo "gh: download failed" >&2; exit 1; }
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) dir="$2"; shift ;;
    --pattern) pattern="$2"; shift ;;
  esac
  shift
done
cp "$GLORP_TEST_ARCHIVE" "$dir/$pattern"
`

// fakeInstallCurl stands in for curl. Asked for a /releases/latest URL it is
// the release lookup: it logs its arguments to GLORP_TEST_LOOKUP_LOG, then
// either fails, when GLORP_TEST_DIRECT_LOOKUP is "fail", or prints
// GLORP_TEST_REDIRECT the way -w '%{redirect_url}' prints where the page
// redirects. With -o it logs them to GLORP_TEST_LOG, then either copies the
// test archive to -o or fails, as GLORP_TEST_DIRECT says.
const fakeInstallCurl = `#!/bin/sh
args="$*"
case "$args" in
  */releases/latest*)
    echo "curl $args" >> "$GLORP_TEST_LOOKUP_LOG"
    [ "$GLORP_TEST_DIRECT_LOOKUP" = fail ] && { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
    printf '%s' "$GLORP_TEST_REDIRECT"
    exit 0
    ;;
esac
out=
while [ $# -gt 0 ]; do
  [ "$1" = -o ] && out="$2"
  shift
done
echo "curl $args" >> "$GLORP_TEST_LOG"
[ "$GLORP_TEST_DIRECT" = fail ] && { echo "curl: download failed" >&2; exit 22; }
cp "$GLORP_TEST_ARCHIVE" "$out"
`

// fakeInstalledGlorp is the glorp binary in the test archive. It answers the
// installer's `glorp agents -skills` with one skills target.
const fakeInstalledGlorp = "#!/bin/sh\necho claude-code\n"

// install.sh downloads the release with gh when gh is on PATH and falls back
// to curl when gh is missing or its download fails, the same policy and the
// same requests as the updater, and fails only when curl fails too.
func TestInstallShellScriptDownloadsWithGHFirst(t *testing.T) {
	script, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	const repo, tag = "acme/glorp-fork", "v1.2.3"
	updaterGH, updaterURL := updaterReleaseRequests(t, repo, tag, runtime.GOOS, runtime.GOARCH)
	archive := filepath.Join(t.TempDir(), "glorp.tar.gz")
	writeFakeGlorpTarball(t, archive, fakeInstalledGlorp)

	for _, tc := range []struct {
		name               string
		gh                 string // "ok", "fail", or "" for no gh on PATH
		directFails        bool
		wantGH, wantDirect bool
		wantInstalled      bool
	}{
		{name: "gh downloads", gh: "ok", wantGH: true, wantInstalled: true},
		{name: "gh fails", gh: "fail", wantGH: true, wantDirect: true, wantInstalled: true},
		{name: "gh missing", wantDirect: true, wantInstalled: true},
		{name: "both fail", gh: "fail", directFails: true, wantGH: true, wantDirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			bin := installerToolsDir(t, work)
			writeFakeTool(t, filepath.Join(bin, "curl"), fakeInstallCurl)
			writeFakeTool(t, filepath.Join(bin, "npx"), "#!/bin/sh\nexit 0\n")
			if tc.gh != "" {
				writeFakeTool(t, filepath.Join(bin, "gh"), fakeInstallGH)
			}
			installDir := filepath.Join(work, "installed")
			log := filepath.Join(work, "downloads.log")
			direct := "ok"
			if tc.directFails {
				direct = "fail"
			}
			cmd := exec.Command(filepath.Join(bin, "bash"), script)
			cmd.Env = []string{
				"PATH=" + bin,
				"HOME=" + work,
				"GLORP_REPO=" + repo,
				"GLORP_VERSION=" + tag,
				"GLORP_BIN_DIR=" + installDir,
				"GLORP_TEST_LOG=" + log,
				"GLORP_TEST_ARCHIVE=" + archive,
				"GLORP_TEST_GH=" + tc.gh,
				"GLORP_TEST_DIRECT=" + direct,
			}
			output, err := cmd.CombinedOutput()
			if tc.wantInstalled && err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			if !tc.wantInstalled && err == nil {
				t.Fatalf("install succeeded with every download failing:\n%s", output)
			}
			assertInstallerDownloads(t, log, "curl", tc.wantGH, tc.wantDirect, updaterGH, updaterURL)
			if got := strings.Contains(string(output), "downloading it directly"); got != (tc.gh == "fail") {
				t.Errorf("output mentions the fallback = %v, want %v:\n%s", got, tc.gh == "fail", output)
			}
			installed := filepath.Join(installDir, "glorp")
			if !tc.wantInstalled {
				if _, err := os.Stat(installed); err == nil {
					t.Fatal("glorp was installed although every download failed")
				}
				return
			}
			assertFileBytes(t, installed, []byte(fakeInstalledGlorp))
			if want := "Installed glorp " + tag; !strings.Contains(string(output), want) {
				t.Fatalf("output does not report %q:\n%s", want, output)
			}
		})
	}
}

// install.sh resolves the latest release from where
// github.com/<repo>/releases/latest redirects, with one curl request and no
// GitHub API call, so it works with the API quota exhausted and without gh. A
// pinned GLORP_VERSION skips the lookup, and a redirect that names no release
// tag fails clearly.
func TestInstallShellScriptResolvesLatestFromTheRedirect(t *testing.T) {
	script, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	const repo, tag = "acme/glorp-fork", "v1.2.3"
	archive := filepath.Join(t.TempDir(), "glorp.tar.gz")
	writeFakeGlorpTarball(t, archive, fakeInstalledGlorp)

	for _, tc := range []struct {
		name          string
		gh            bool
		version       string
		redirect      string
		lookupFails   bool
		wantLookup    bool
		wantInstalled bool
	}{
		{name: "redirect names the tag", gh: true, redirect: "https://github.com/" + repo + "/releases/tag/" + tag, wantLookup: true, wantInstalled: true},
		{name: "no gh", redirect: "https://github.com/" + repo + "/releases/tag/" + tag, wantLookup: true, wantInstalled: true},
		{name: "explicit latest", version: "latest", redirect: "https://github.com/" + repo + "/releases/tag/" + tag, wantLookup: true, wantInstalled: true},
		{name: "pinned version", gh: true, version: tag, wantInstalled: true},
		{name: "lookup fails", lookupFails: true, wantLookup: true},
		{name: "no release", redirect: "https://github.com/" + repo + "/releases", wantLookup: true},
		{name: "no redirect", wantLookup: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			bin := installerToolsDir(t, work)
			writeFakeTool(t, filepath.Join(bin, "curl"), fakeInstallCurl)
			writeFakeTool(t, filepath.Join(bin, "npx"), "#!/bin/sh\nexit 0\n")
			if tc.gh {
				writeFakeTool(t, filepath.Join(bin, "gh"), fakeInstallGH)
			}
			installDir := filepath.Join(work, "installed")
			lookups := filepath.Join(work, "lookups.log")
			directLookup := "ok"
			if tc.lookupFails {
				directLookup = "fail"
			}
			cmd := exec.Command(filepath.Join(bin, "bash"), script)
			cmd.Env = []string{
				"PATH=" + bin,
				"HOME=" + work,
				"GLORP_REPO=" + repo,
				"GLORP_VERSION=" + tc.version,
				"GLORP_BIN_DIR=" + installDir,
				"GLORP_TEST_LOG=" + filepath.Join(work, "downloads.log"),
				"GLORP_TEST_LOOKUP_LOG=" + lookups,
				"GLORP_TEST_ARCHIVE=" + archive,
				"GLORP_TEST_REDIRECT=" + tc.redirect,
				"GLORP_TEST_GH=ok",
				"GLORP_TEST_DIRECT_LOOKUP=" + directLookup,
			}
			output, err := cmd.CombinedOutput()
			if tc.wantInstalled && err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			if !tc.wantInstalled && err == nil {
				t.Fatalf("install succeeded without resolving a release:\n%s", output)
			}
			assertRedirectLookups(t, lookups, "curl", repo, tc.wantLookup)
			installed := filepath.Join(installDir, "glorp")
			if !tc.wantInstalled {
				if !strings.Contains(string(output), "could not resolve latest release") {
					t.Errorf("output does not say the release could not be resolved:\n%s", output)
				}
				if _, err := os.Stat(installed); err == nil {
					t.Fatal("glorp was installed although no release was resolved")
				}
				return
			}
			assertFileBytes(t, installed, []byte(fakeInstalledGlorp))
			if want := "Installed glorp " + tag; !strings.Contains(string(output), want) {
				t.Fatalf("output does not report %q:\n%s", want, output)
			}
		})
	}
}

// installerToolsDir links the real tools install.sh needs into a directory of
// its own, so PATH can hold exactly them plus the fakes and no installed gh.
func installerToolsDir(t *testing.T, work string) string {
	t.Helper()
	bin := filepath.Join(work, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"bash", "cat", "cp", "gzip", "head", "install", "mkdir", "mktemp", "rm", "sed", "tar", "tr", "uname"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("install.sh needs %s: %v", tool, err)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func writeFakeTool(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeFakeGlorpTarball writes a release-shaped archive holding a glorp
// executable with content.
func writeFakeGlorpTarball(t *testing.T, path, content string) {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	err := archive.WriteHeader(&tar.Header{Name: "glorp", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	if err == nil {
		_, err = archive.Write([]byte(content))
	}
	if err == nil {
		err = archive.Close()
	}
	if err == nil {
		err = compressed.Close()
	}
	if err == nil {
		err = os.WriteFile(path, buffer.Bytes(), 0o644)
	}
	if err != nil {
		t.Fatal(err)
	}
}
