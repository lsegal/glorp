package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lsegal/glorp/browser"
)

func readDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// authSection returns the README's `### `glorp auth“ section, so a test can
// assert against the documented command rather than any stray mention of it
// elsewhere in the file.
func authSection(t *testing.T) string {
	t.Helper()
	readme := readDoc(t, "README.md")
	start := strings.Index(readme, "### `glorp auth`")
	if start < 0 {
		t.Fatal("README.md has no `### `glorp auth`` section")
	}
	rest := readme[start+len("### `glorp auth`"):]
	if end := strings.Index(rest, "\n### "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// TestReadmeDocumentsEveryAuthFlag keeps the README's flag table in step with
// the flag set `glorp help auth` prints: a flag that ships undocumented, or a
// documented flag that was renamed away, is the failure #390 exists to avoid.
func TestReadmeDocumentsEveryAuthFlag(t *testing.T) {
	section := authSection(t)
	authFlagSet().VisitAll(func(f *flag.Flag) {
		if !strings.Contains(section, "`--"+f.Name) {
			t.Errorf("README `glorp auth` section does not document the -%s flag", f.Name)
		}
	})
}

// TestReadmeAuthProfilePathsMatchLauncher pins the per-OS profile paths in the
// docs to the directory browser.ProfileDir actually builds.
func TestReadmeAuthProfilePathsMatchLauncher(t *testing.T) {
	section := authSection(t)
	for _, path := range []string{
		"~/Library/Application Support/glorp/" + browser.ProfileName,
		`%AppData%\glorp\` + browser.ProfileName,
		"~/.config/glorp/" + browser.ProfileName,
	} {
		if !strings.Contains(section, path) {
			t.Errorf("README `glorp auth` section does not document the profile path %s", path)
		}
	}
	dir, err := browser.ProfileDir("")
	if err != nil {
		t.Fatalf("browser.ProfileDir: %v", err)
	}
	if got := filepath.Base(dir); got != browser.ProfileName {
		t.Errorf("browser.ProfileDir(\"\") ends in %q, want %q", got, browser.ProfileName)
	}
	if got := filepath.Base(filepath.Dir(dir)); got != "glorp" {
		t.Errorf("browser.ProfileDir(\"\") parent is %q, want \"glorp\"", got)
	}
}

// TestDocsCoverBrowserSignIn covers the parts of #390 outside the command's own
// section: the one-time sign-in step in the prerequisites, the sign-in
// paragraph in "How it works", and the site's browser-mode copy.
func TestDocsCoverBrowserSignIn(t *testing.T) {
	readme := readDoc(t, "README.md")
	for _, required := range []string{
		"glorp auth          # opens a browser window; sign in there",
		"glorp auth --status # confirm it took",
		"one-time step per profile",
		"`glorp watch` itself never opens a window",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README.md does not document the browser sign-in step %q", required)
		}
	}
	site := readDoc(t, filepath.Join("site", "layouts", "index.html"))
	for _, required := range []string{"<code>glorp auth</code>", "<code>glorp auth --status</code>"} {
		if !strings.Contains(site, required) {
			t.Errorf("site/layouts/index.html does not mention %s", required)
		}
	}
}

// TestAuthDocsDoNotPromiseAutomaticSignIn guards the acceptance criterion that
// the docs describe only what ships: the automatic headed-login recovery from
// #379 is not implemented, so nothing may claim `glorp watch` opens a window.
func TestAuthDocsDoNotPromiseAutomaticSignIn(t *testing.T) {
	for _, path := range []string{"README.md", filepath.Join("site", "layouts", "index.html")} {
		body := strings.ToLower(readDoc(t, path))
		for _, forbidden := range []string{
			"automatically opens a sign-in",
			"pops the login window",
			"opens a login window automatically",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s promises automatic sign-in recovery, which does not ship: %q", path, forbidden)
			}
		}
	}
}
