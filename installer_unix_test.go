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

// fakeInstallGH stands in for gh: it logs its arguments, then either copies the
// test archive to --dir/--pattern the way `gh release download` would or
// fails, as GLORP_TEST_GH says.
const fakeInstallGH = `#!/bin/sh
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

// fakeInstallCurl stands in for curl: it logs its arguments, then either
// copies the test archive to -o or fails, as GLORP_TEST_DIRECT says.
const fakeInstallCurl = `#!/bin/sh
echo "curl $*" >> "$GLORP_TEST_LOG"
[ "$GLORP_TEST_DIRECT" = fail ] && { echo "curl: download failed" >&2; exit 22; }
while [ $# -gt 0 ]; do
  [ "$1" = -o ] && out="$2"
  shift
done
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
