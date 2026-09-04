package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lsegal/glorp/agents"
)

// definitionWithQuota builds a minimal valid definition carrying quota.
func definitionWithQuota(t *testing.T, name string, quota agents.Quota) agents.Definition {
	t.Helper()
	return agents.Definition{
		Name: name, Binary: name, Quota: quota,
		Args:    agents.Args{Run: []agents.Fragment{{Args: []string{"{prompt}"}}}, Resume: []agents.Fragment{{Args: []string{"{prompt}"}}}},
		Session: agents.Session{Assign: agents.AssignNone},
		Output:  agents.Output{Format: agents.FormatText},
	}
}

func registryWith(t *testing.T, definitions ...agents.Definition) *agents.Registry {
	t.Helper()
	registry, err := agents.NewRegistry(definitions...)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// fakeQuotaBinary writes a shell script that prints body on stdout and returns
// its path, so the generic reader can be exercised against a real process.
func fakeQuotaBinary(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake quota binary needs a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-quota")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNamedQuotaReadersComeFromTheDefinition checks the reader an agent gets
// is chosen by its quota block rather than by its name, which is what lets a
// config-defined agent report a quota at all.
func TestNamedQuotaReadersComeFromTheDefinition(t *testing.T) {
	registry := registryWith(t,
		definitionWithQuota(t, "codex", agents.Quota{Reader: agents.QuotaCodex}),
		definitionWithQuota(t, "claude", agents.Quota{Reader: agents.QuotaClaude}),
		definitionWithQuota(t, "muse", agents.Quota{Reader: agents.QuotaCommand, Command: []string{"muse", "usage"}, PercentUsed: "used"}),
		definitionWithQuota(t, "quiet", agents.Quota{}),
	)
	readers := namedQuotaReaders(registry, []string{"codex", "claude", "muse", "quiet", "codex", "stranger"}, func(string) string { return "bin" })
	var names []string
	for _, reader := range readers {
		names = append(names, reader.name)
	}
	if got := strings.Join(names, ","); got != "codex,claude,muse,quiet,stranger" {
		t.Fatalf("reader names = %q, want the configured agents deduplicated in order", got)
	}
}

// TestQuotaReadersWithoutASourceSpawnNothing pins the rule that an agent that
// declares no quota costs no process on every poll: the reader an unknown or
// quota-less agent gets reports empty without ever running anything. A reader
// that shelled out would be visible here as a non-empty reading, since the
// executable named does not exist.
func TestQuotaReadersWithoutASourceSpawnNothing(t *testing.T) {
	registry := registryWith(t, definitionWithQuota(t, "quiet", agents.Quota{}))
	binaryCalls := 0
	readers := namedQuotaReaders(registry, []string{"quiet", "stranger"}, func(string) string {
		binaryCalls++
		return "definitely-not-a-real-binary"
	})
	for _, reader := range readers {
		if got := reader.read(context.Background()); got != "" {
			t.Fatalf("%s quota = %q, want untracked", reader.name, got)
		}
	}
	if binaryCalls != 0 {
		t.Fatalf("resolved %d binaries for agents with no quota source, want 0", binaryCalls)
	}
	if got := formatNamedQuotas(map[string]string{"quiet": "", "stranger": ""}); got != "quiet: not tracked, stranger: not tracked" {
		t.Fatalf("formatted = %q", got)
	}
}

// TestCommandQuotaReaderFormatsConfiguredFields runs the generic reader
// against a fake binary and checks the configured fields reach the template.
func TestCommandQuotaReaderFormatsConfiguredFields(t *testing.T) {
	binary := fakeQuotaBinary(t, `printf '{"limits":{"used":37,"resets":"Fri 3pm"}}'`)
	spec := agents.Quota{
		Reader: agents.QuotaCommand, Command: []string{"{binary}", "usage", "--json"},
		PercentUsed: "limits.used", ResetAt: "limits.resets",
		Format: "{percentLeft}% left ({percentUsed}% used) until {resetAt}",
	}
	got, err := readCommandQuota(context.Background(), binary, spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := "63% left (37% used) until Fri 3pm"; got != want {
		t.Fatalf("quota = %q, want %q", got, want)
	}
}

// TestCommandQuotaReaderDefaultsToPercentLeft checks the default template, so
// an agent only has to name the field its CLI reports.
func TestCommandQuotaReaderDefaultsToPercentLeft(t *testing.T) {
	binary := fakeQuotaBinary(t, `printf '{"used":"12.4%%"}'`)
	spec := agents.Quota{Reader: agents.QuotaCommand, Command: []string{"{binary}"}, PercentUsed: "used"}
	got, err := readCommandQuota(context.Background(), binary, spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := "88% left"; got != want {
		t.Fatalf("quota = %q, want %q", got, want)
	}
}

// TestCommandQuotaReaderRejectsMalformedOutput covers the paths a changed or
// broken agent CLI takes: output that is not JSON, output missing the field
// the definition points at, and a command that fails outright.
func TestCommandQuotaReaderRejectsMalformedOutput(t *testing.T) {
	spec := func(binary string) agents.Quota {
		return agents.Quota{Reader: agents.QuotaCommand, Command: []string{binary}, PercentUsed: "limits.used"}
	}
	cases := []struct {
		name, body, want string
	}{
		{name: "not json", body: `printf 'usage: 40%%'`, want: "did not print JSON"},
		{name: "missing field", body: `printf '{"limits":{}}'`, want: `"limits.used" is missing`},
		{name: "wrong type", body: `printf '{"limits":{"used":{"pct":4}}}'`, want: `"limits.used" is missing`},
		{name: "command fails", body: `echo boom >&2; exit 3`, want: "run quota command"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			binary := fakeQuotaBinary(t, test.body)
			got, err := readCommandQuota(context.Background(), binary, spec(binary))
			if err == nil {
				t.Fatalf("quota = %q, want an error", got)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
		})
	}
}

// TestCommandQuotaReaderTimesOut checks a quota command that hangs is
// abandoned rather than allowed to hold up the poll that asked for it.
func TestCommandQuotaReaderTimesOut(t *testing.T) {
	binary := fakeQuotaBinary(t, "sleep 30")
	spec := agents.Quota{Reader: agents.QuotaCommand, Command: []string{binary}, PercentUsed: "used", Timeout: "150ms"}
	start := time.Now()
	if got, err := readCommandQuota(context.Background(), binary, spec); err == nil {
		t.Fatalf("quota = %q, want a timeout error", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("read took %s, want it abandoned at the configured timeout", elapsed)
	}
}

// TestCommandQuotaReaderKeepsTheLastGoodReading checks a failed read leaves
// the previous reading in the status bar instead of blanking it, and that the
// one-minute cache stops a second poll re-running the command.
func TestCommandQuotaReaderKeepsTheLastGoodReading(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	binary := fakeQuotaBinary(t, fmt.Sprintf(`printf 'x' >> %q; printf '{"used":25}'`, counter))
	reader := &commandQuotaReader{Binary: binary, Spec: agents.Quota{
		Reader: agents.QuotaCommand, Command: []string{"{binary}"}, PercentUsed: "used",
	}}
	if got := reader.Read(context.Background()); got != "75% left" {
		t.Fatalf("first read = %q", got)
	}
	if got := reader.Read(context.Background()); got != "75% left" {
		t.Fatalf("cached read = %q", got)
	}
	runs, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("ran the quota command %d times within the cache window, want 1", len(runs))
	}
	// A later failure must not replace the good reading with an empty one.
	reader.readAt = time.Time{}
	reader.Spec.Command = []string{filepath.Join(dir, "gone")}
	if got := reader.Read(context.Background()); got != "75% left" {
		t.Fatalf("read after a failure = %q, want the last good reading", got)
	}
}
