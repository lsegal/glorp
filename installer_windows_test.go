package main

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installerHarness runs install.ps1 against archives built in the test instead
// of a GitHub release. The wrapper shadows the cmdlets and commands the script
// reaches the network or the user's tools with, and restores the user Path the
// script appends the install directory to. GLORP_TEST_BIN holds a fake gh.cmd
// and goes first on Path; without it Path keeps only Windows' own directories,
// so no installed gh is found either.
const installerHarness = `$ErrorActionPreference = 'Stop'
function npx {}
function Invoke-WebRequest {
    param($Uri, $OutFile)
    Add-Content -Path $env:GLORP_TEST_LOG -Value "Invoke-WebRequest $Uri"
    if ($env:GLORP_TEST_DIRECT -eq 'fail') { throw 'direct download failed' }
    Copy-Item $env:GLORP_TEST_ARCHIVE $OutFile
}
if ($env:GLORP_TEST_BIN) {
    $env:Path = "$env:GLORP_TEST_BIN;$env:Path"
} else {
    $env:Path = "$env:SystemRoot\System32;$PSHOME"
}
$savedPath = [Environment]::GetEnvironmentVariable('Path', 'User')
try {
    & $env:GLORP_TEST_SCRIPT
} finally {
    [Environment]::SetEnvironmentVariable('Path', $savedPath, 'User')
}
`

// fakeInstallGH stands in for gh as a real native command: it logs its
// arguments, then either copies the test archive to --dir\--pattern the way
// `gh release download` would or writes to stderr and exits 1, as
// GLORP_TEST_GH says.
const fakeInstallGH = `@echo off
>>"%GLORP_TEST_LOG%" echo gh %*
if not "%GLORP_TEST_GH%"=="ok" (
  echo gh: download failed 1>&2
  exit /b 1
)
:next
if "%~1"=="" goto done
if "%~1"=="--dir" set "dir=%~2"
if "%~1"=="--pattern" set "pattern=%~2"
shift
goto next
:done
copy /y "%GLORP_TEST_ARCHIVE%" "%dir%\%pattern%" >nul
`

// Windows will not overwrite a running glorp.exe, so the installer renames it
// out of the way. After one in-place upgrade the still-running process's
// image is glorp.exe.bak, which cannot be deleted, so a second upgrade must
// pick another backup name instead of failing on the locked one.
func TestInstallPowerShellUpgradesWhileGlorpRuns(t *testing.T) {
	script, err := filepath.Abs("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	harness := filepath.Join(work, "harness.ps1")
	if err := os.WriteFile(harness, []byte(installerHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(work, "bin")
	installedExe := filepath.Join(installDir, "glorp.exe")

	builds := map[string][]byte{}
	archives := map[string]string{}
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"} {
		archives[version], builds[version] = buildFakeGlorpArchive(t, work, version)
	}
	install := func(version string) {
		t.Helper()
		output, err := runInstallPowerShell(harness, script,
			"GLORP_TEST_ARCHIVE="+archives[version],
			"GLORP_TEST_LOG="+filepath.Join(work, "downloads.log"),
			"GLORP_VERSION=v"+version,
			"GLORP_BIN_DIR="+installDir,
		)
		if err != nil {
			t.Fatalf("install v%s failed: %v\n%s", version, err, output)
		}
		if want := "Installed glorp v" + version; !strings.Contains(string(output), want) {
			t.Fatalf("install v%s output does not report %q:\n%s", version, want, output)
		}
		reported, err := exec.Command(installedExe, "version").Output()
		if err != nil {
			t.Fatalf("run installed glorp.exe: %v", err)
		}
		if got := strings.TrimSpace(string(reported)); got != version {
			t.Fatalf("installed glorp.exe reports %q after installing v%s", got, version)
		}
		assertFileBytes(t, installedExe, builds[version])
	}

	install("1.0.0")
	running := exec.Command(installedExe, "serve")
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		running.Wait()
		close(exited)
	}()
	stop := func() {
		running.Process.Kill()
		<-exited
	}
	t.Cleanup(stop)
	stillRunning := func() {
		t.Helper()
		select {
		case <-exited:
			t.Fatal("the running glorp exited during the install")
		default:
		}
	}

	install("2.0.0")
	stillRunning()
	assertFileBytes(t, filepath.Join(installDir, "glorp.exe.bak"), builds["1.0.0"])

	// The running v1 image is glorp.exe.bak now and cannot be removed.
	install("3.0.0")
	stillRunning()
	assertFileBytes(t, filepath.Join(installDir, "glorp.exe.bak"), builds["1.0.0"])
	backups := fakeGlorpBackups(t, installDir)
	if len(backups) != 2 {
		t.Fatalf("backups after the second running upgrade = %v, want glorp.exe.bak and one more", backups)
	}
	for _, backup := range backups {
		if backup != "glorp.exe.bak" {
			assertFileBytes(t, filepath.Join(installDir, backup), builds["2.0.0"])
		}
	}

	// Once nothing runs from them, stale backups are cleaned up.
	stop()
	install("4.0.0")
	if backups := fakeGlorpBackups(t, installDir); len(backups) != 1 || backups[0] != "glorp.exe.bak" {
		t.Fatalf("backups after the process exited = %v, want only glorp.exe.bak", backups)
	}
	assertFileBytes(t, filepath.Join(installDir, "glorp.exe.bak"), builds["3.0.0"])
}

// install.ps1 downloads the release with gh when gh is on Path and falls back
// to Invoke-WebRequest when gh is missing or its download fails, the same
// policy and the same requests as the updater, and fails only when the direct
// download fails too. gh is not required up front, so a machine without it
// still installs.
func TestInstallPowerShellDownloadsWithGHFirst(t *testing.T) {
	script, err := filepath.Abs("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	harness := filepath.Join(work, "harness.ps1")
	if err := os.WriteFile(harness, []byte(installerHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	const repo, tag = "acme/glorp-fork", "v1.2.3"
	architecture := "amd64"
	if runtime.GOARCH == "arm64" {
		architecture = "arm64"
	}
	updaterGH, updaterURL := updaterReleaseRequests(t, repo, tag, "windows", architecture)
	archive, build := buildFakeGlorpArchive(t, work, "1.2.3")

	for _, tc := range []struct {
		name               string
		gh                 string // "ok", "fail", or "" for no gh on Path
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
			caseDir := t.TempDir()
			installDir := filepath.Join(caseDir, "installed")
			log := filepath.Join(caseDir, "downloads.log")
			direct, bin := "ok", ""
			if tc.directFails {
				direct = "fail"
			}
			if tc.gh != "" {
				bin = filepath.Join(caseDir, "bin")
				if err := os.MkdirAll(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "gh.cmd"), []byte(fakeInstallGH), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			output, err := runInstallPowerShell(harness, script,
				"GLORP_REPO="+repo,
				"GLORP_VERSION="+tag,
				"GLORP_BIN_DIR="+installDir,
				"GLORP_TEST_ARCHIVE="+archive,
				"GLORP_TEST_LOG="+log,
				"GLORP_TEST_BIN="+bin,
				"GLORP_TEST_GH="+tc.gh,
				"GLORP_TEST_DIRECT="+direct,
			)
			if tc.wantInstalled && err != nil {
				t.Fatalf("install failed: %v\n%s", err, output)
			}
			if !tc.wantInstalled && err == nil {
				t.Fatalf("install succeeded with every download failing:\n%s", output)
			}
			assertInstallerDownloads(t, log, "Invoke-WebRequest", tc.wantGH, tc.wantDirect, updaterGH, updaterURL)
			if got := strings.Contains(string(output), "downloading it directly"); got != (tc.gh == "fail") {
				t.Errorf("output mentions the fallback = %v, want %v:\n%s", got, tc.gh == "fail", output)
			}
			installed := filepath.Join(installDir, "glorp.exe")
			if !tc.wantInstalled {
				if _, err := os.Stat(installed); err == nil {
					t.Fatal("glorp.exe was installed although every download failed")
				}
				return
			}
			assertFileBytes(t, installed, build)
			if want := "Installed glorp " + tag; !strings.Contains(string(output), want) {
				t.Fatalf("output does not report %q:\n%s", want, output)
			}
		})
	}
}

// runInstallPowerShell runs install.ps1 through the harness with env added to
// the test's environment.
func runInstallPowerShell(harness, script string, env ...string) ([]byte, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", harness)
	cmd.Env = append(append(os.Environ(), "GLORP_TEST_SCRIPT="+script), env...)
	return cmd.CombinedOutput()
}

// buildFakeGlorpArchive builds testdata/fakeglorp stamped with version and
// zips it the way a release archive holds glorp.exe.
func buildFakeGlorpArchive(t *testing.T, work, version string) (string, []byte) {
	t.Helper()
	dir := filepath.Join(work, "v"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "glorp.exe")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", binary, "./testdata/fakeglorp")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake glorp v%s: %v\n%s", version, err, output)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "glorp.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("glorp.exe")
	if err == nil {
		_, err = entry.Write(data)
	}
	if err == nil {
		err = writer.Close()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("write archive for v%s: %v", version, err)
	}
	return archive, data
}

// fakeGlorpBackups lists the backup names the installer left in dir, sorted.
func fakeGlorpBackups(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "glorp.exe*.bak"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	return uniqueSorted(names)
}
