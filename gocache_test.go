package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goCacheAction is the composite action every Go-using workflow job delegates
// its caching to, referenced by the path a workflow `uses:`.
const goCacheAction = "./.github/actions/go-cache"

// goWorkflows returns every workflow file that sets Go up, so a workflow added
// later is held to the same caching rules without this test being edited.
func goWorkflows(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(".github", "workflows"))
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".yml" && filepath.Ext(name) != ".yaml" {
			continue
		}
		body := readWorkflow(t, name)
		if strings.Contains(body, "actions/setup-go@") {
			found[name] = body
		}
	}
	if len(found) == 0 {
		t.Fatal("no workflow sets Go up; this test can no longer catch an uncached build")
	}
	return found
}

// A Go job that starts cold rebuilds the standard library and every dependency
// before it runs a single test, so every workflow that sets Go up has to cache
// it. setup-go's built-in cache is not enough on its own -- see the test below
// -- so each job routes through the shared composite action instead.
func TestGoWorkflowsCacheTheirBuilds(t *testing.T) {
	for name, body := range goWorkflows(t) {
		if !strings.Contains(body, goCacheAction) {
			t.Errorf("workflow %s sets Go up but does not use %s, so its builds start cold", name, goCacheAction)
		}
	}
}

// Both setup-go's cache and the composite action store GOMODCACHE and GOCACHE.
// Leaving both on has them saving the same directories under two keys, which
// doubles the upload and lets the weaker of the two win a restore, so the
// built-in one is turned off wherever the composite action is used.
func TestGoWorkflowsDisableTheBuiltInSetupGoCache(t *testing.T) {
	for name, body := range goWorkflows(t) {
		setups := strings.Count(body, "actions/setup-go@")
		if disabled := strings.Count(body, "cache: false"); disabled != setups {
			t.Errorf("workflow %s sets Go up %d times but disables setup-go's cache %d times", name, setups, disabled)
		}
	}
}

// The point of replacing setup-go's cache is the fallback it lacks: without
// restore-keys, any change to the key is a fully cold build. Keying on the Go
// sources as well as go.sum is what keeps the compiled objects for this
// repository's own packages fresh rather than frozen at the last dependency
// bump.
func TestGoCacheActionRestoresOnANearMiss(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(".github", "actions", "go-cache", "action.yml"))
	if err != nil {
		t.Fatalf("read the go-cache action: %v", err)
	}
	body := strings.ReplaceAll(string(data), "\r\n", "\n")

	for _, want := range []string{"GOMODCACHE", "GOCACHE"} {
		if !strings.Contains(body, want) {
			t.Errorf("the go-cache action must cache %s", want)
		}
	}
	if !strings.Contains(body, "restore-keys:") {
		t.Error("the go-cache action must set restore-keys, or a key change is a cold build")
	}
	for _, want := range []string{"hashFiles('**/go.sum')", "hashFiles('**/*.go')"} {
		if !strings.Contains(body, want) {
			t.Errorf("the go-cache action's key must cover %s", want)
		}
	}
}
