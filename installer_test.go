package main

import (
	"os"
	"strings"
	"testing"
)

// install.sh is documented to be run as `curl -fsSL ... | bash`, so bash reads
// the script itself from stdin. Any command in it that reads stdin consumes the
// rest of the script, which is how the final "Installed glorp ..." line went
// missing. Keep every npx call detached from the pipe.
func TestInstallShellScriptKeepsNpxOffStdin(t *testing.T) {
	data, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	var npxLines int
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "npx ") {
			continue
		}
		npxLines++
		if !strings.Contains(trimmed, "</dev/null") {
			t.Errorf("npx call must redirect stdin from /dev/null, got: %s", trimmed)
		}
	}
	if npxLines == 0 {
		t.Fatal("install.sh no longer contains any npx calls")
	}
	if !strings.Contains(string(data), `echo "Installed glorp $version`) {
		t.Error("install.sh must still report the installed version")
	}
}
