package main

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installerHarness runs install.ps1 against archives built in the test instead
// of a GitHub release. The wrapper shadows the cmdlets and commands the script
// reaches the network or the user's tools with, and restores the user Path the
// script appends the install directory to.
const installerHarness = `$ErrorActionPreference = 'Stop'
function gh {}
function npx {}
function Invoke-WebRequest { param($Uri, $OutFile) Copy-Item $env:GLORP_TEST_ARCHIVE $OutFile }
$savedPath = [Environment]::GetEnvironmentVariable('Path', 'User')
try {
    & $env:GLORP_TEST_SCRIPT
} finally {
    [Environment]::SetEnvironmentVariable('Path', $savedPath, 'User')
}
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
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", harness)
		cmd.Env = append(os.Environ(),
			"GLORP_TEST_SCRIPT="+script,
			"GLORP_TEST_ARCHIVE="+archives[version],
			"GLORP_VERSION=v"+version,
			"GLORP_BIN_DIR="+installDir,
		)
		output, err := cmd.CombinedOutput()
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

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not the expected build", filepath.Base(path))
	}
}
