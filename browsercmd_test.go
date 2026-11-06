package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lsegal/glorp/browser"
)

// withBrowserWindow swaps in a window that records the config it was opened
// with and returns err at once.
func withBrowserWindow(t *testing.T, err error) *[]browser.Config {
	t.Helper()
	var configs []browser.Config
	original := openBrowserWindow
	openBrowserWindow = func(_ context.Context, config browser.Config) error {
		configs = append(configs, config)
		return err
	}
	t.Cleanup(func() { openBrowserWindow = original })
	return &configs
}

func TestBrowserCommandIsRegistered(t *testing.T) {
	cmd, ok := lookupCommand("browser")
	if !ok {
		t.Fatal("no browser command registered; `glorp browser` and `glorp help browser` do not work")
	}
	if cmd.run == nil {
		t.Fatal("browser command has no run function")
	}
	if !strings.Contains(cmd.usage, "glorp browser") {
		t.Fatalf("browser usage does not name the command: %q", cmd.usage)
	}
	flags := commandFlags("browser")
	if flags == nil {
		t.Fatal(`commandFlags("browser") is nil, so ` + "`glorp help browser`" + ` prints no flags`)
	}
	for _, name := range []string{"browser-profile", "browser-binary"} {
		if flags.Lookup(name) == nil {
			t.Errorf("browser flag set has no -%s", name)
		}
	}
	var out strings.Builder
	printUsage(&out)
	if !strings.Contains(out.String(), "browser") {
		t.Fatalf("top-level usage does not list browser:\n%s", out.String())
	}
}

func TestBrowserWindowOpensHeadedOnTheConfiguredProfile(t *testing.T) {
	withHeadedEnvironment(t, nil)
	configs := withBrowserWindow(t, nil)
	var out strings.Builder
	err := browserWindow(context.Background(), browser.Config{Profile: "/tmp/profile", Binary: "chromium"}, &out)
	if err != nil {
		t.Fatalf("browserWindow() error = %v", err)
	}
	if len(*configs) != 1 {
		t.Fatalf("opened %d windows, want 1", len(*configs))
	}
	got := (*configs)[0]
	if !got.Headed || got.Profile != "/tmp/profile" || got.Binary != "chromium" {
		t.Fatalf("opened with %+v, want a headed window on the given profile and binary", got)
	}
	if !strings.Contains(out.String(), "/tmp/profile") {
		t.Fatalf("output does not name the profile: %q", out.String())
	}
}

func TestBrowserWindowReportsAFailureToOpen(t *testing.T) {
	withHeadedEnvironment(t, nil)
	want := errors.New("profile in use")
	withBrowserWindow(t, want)
	if err := browserWindow(context.Background(), browser.Config{}, &strings.Builder{}); !errors.Is(err, want) {
		t.Fatalf("browserWindow() error = %v, want %v", err, want)
	}
}

func TestBrowserWindowRefusesWithoutADisplay(t *testing.T) {
	withHeadedEnvironment(t, errors.New("no display"))
	configs := withBrowserWindow(t, nil)
	err := browserWindow(context.Background(), browser.Config{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "no display server") {
		t.Fatalf("browserWindow() error = %v, want the no-display refusal", err)
	}
	if strings.Contains(err.Error(), "sign-in") {
		t.Fatalf("refusal talks about signing in: %v", err)
	}
	if len(*configs) != 0 {
		t.Fatalf("opened a window with no display: %+v", *configs)
	}
}

func TestRunBrowserCommandRejectsArguments(t *testing.T) {
	configs := withBrowserWindow(t, nil)
	if code := runBrowserCommand([]string{"owner/repo"}); code != 2 {
		t.Fatalf("runBrowserCommand() = %d, want 2", code)
	}
	if len(*configs) != 0 {
		t.Fatalf("opened a window for a bad invocation: %+v", *configs)
	}
}
