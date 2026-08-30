package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeBrowserLookup replaces browser discovery for the duration of a test so no
// test depends on which browsers happen to be installed on the machine running
// it, and none of them can launch one.
func fakeBrowserLookup(t *testing.T, onPath map[string]string, installed map[string]bool) {
	t.Helper()
	previousLook, previousStat := lookBrowserPath, statBrowserBinary
	t.Cleanup(func() { lookBrowserPath, statBrowserBinary = previousLook, previousStat })
	lookBrowserPath = func(name string) (string, error) {
		if path, ok := onPath[name]; ok {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
	statBrowserBinary = func(path string) error {
		if installed[path] {
			return nil
		}
		return os.ErrNotExist
	}
}

func TestFindBrowserBinaryPrefersPathInOrder(t *testing.T) {
	fakeBrowserLookup(t, map[string]string{
		"chromium": "/usr/bin/chromium",
		"msedge":   "/usr/bin/msedge",
	}, nil)
	path, err := findBrowserBinary("")
	if err != nil {
		t.Fatalf("findBrowserBinary: %v", err)
	}
	if path != "/usr/bin/chromium" {
		t.Fatalf("found %q, want the earlier name in browserBinaryNames", path)
	}
}

func TestFindBrowserBinaryFallsBackToWellKnownPaths(t *testing.T) {
	known := browserBinaryPaths()
	var installed string
	for _, path := range known {
		if path != "" {
			installed = path
			break
		}
	}
	if installed == "" {
		t.Fatalf("browserBinaryPaths returned no usable path on %s", runtime.GOOS)
	}
	fakeBrowserLookup(t, nil, map[string]bool{installed: true})
	path, err := findBrowserBinary("")
	if err != nil {
		t.Fatalf("findBrowserBinary: %v", err)
	}
	if path != installed {
		t.Fatalf("found %q, want %q", path, installed)
	}
}

func TestFindBrowserBinaryReportsActionableErrorWhenMissing(t *testing.T) {
	fakeBrowserLookup(t, nil, nil)
	_, err := findBrowserBinary("")
	if err == nil {
		t.Fatal("expected an error when no browser is installed")
	}
	for _, want := range []string{"--browser-binary", "Chrome"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestFindBrowserBinaryUsesOverride(t *testing.T) {
	fakeBrowserLookup(t, map[string]string{
		"google-chrome": "/usr/bin/google-chrome",
		"my-browser":    "/opt/my-browser",
	}, nil)
	path, err := findBrowserBinary("my-browser")
	if err != nil {
		t.Fatalf("findBrowserBinary: %v", err)
	}
	if path != "/opt/my-browser" {
		t.Fatalf("found %q, want the override", path)
	}
}

func TestFindBrowserBinaryRejectsUnusableOverride(t *testing.T) {
	// A discovered browser must not quietly stand in for the one the user named.
	fakeBrowserLookup(t, map[string]string{"google-chrome": "/usr/bin/google-chrome"}, nil)
	_, err := findBrowserBinary("/nowhere/chrome")
	if err == nil {
		t.Fatal("expected an error for an override that cannot be run")
	}
	if !strings.Contains(err.Error(), "/nowhere/chrome") {
		t.Fatalf("error %q does not name the override", err)
	}
}

func TestBrowserProfileDirUsesOverride(t *testing.T) {
	dir, err := browserProfileDir("/tmp/custom-profile")
	if err != nil {
		t.Fatalf("browserProfileDir: %v", err)
	}
	if dir != "/tmp/custom-profile" {
		t.Fatalf("profile %q, want the override", dir)
	}
}

// TestBrowserProfileDirPerPlatform checks the profile lands in glorp's own
// directory under whichever configuration directory this platform uses.
func TestBrowserProfileDirPerPlatform(t *testing.T) {
	home := t.TempDir()
	var config string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", home)
		config = home
	case "darwin":
		t.Setenv("HOME", home)
		config = filepath.Join(home, "Library", "Application Support")
	default:
		t.Setenv("XDG_CONFIG_HOME", home)
		config = home
	}
	dir, err := browserProfileDir("")
	if err != nil {
		t.Fatalf("browserProfileDir: %v", err)
	}
	want := filepath.Join(config, "glorp", "browser-data")
	if dir != want {
		t.Fatalf("profile %q, want %q", dir, want)
	}
}

func TestBrowserProfileDirReportsConfigDirFailure(t *testing.T) {
	previous := browserConfigDir
	t.Cleanup(func() { browserConfigDir = previous })
	browserConfigDir = func() (string, error) { return "", errors.New("no home") }
	if _, err := browserProfileDir(""); err == nil {
		t.Fatal("expected an error when the configuration directory is unknown")
	}
}

// TestFreeBrowserPortIsUsable checks the reserved port was released again, so
// the browser can bind it, and that repeated reservations do not collide the
// way a fixed port would across concurrent glorp instances.
func TestFreeBrowserPortIsUsable(t *testing.T) {
	port, err := freeBrowserPort()
	if err != nil {
		t.Fatalf("freeBrowserPort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("port %d is not a valid port", port)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("reserved port %d was not released: %v", port, err)
	}
	defer func() { _ = listener.Close() }()

	other, err := freeBrowserPort()
	if err != nil {
		t.Fatalf("freeBrowserPort: %v", err)
	}
	if other == port {
		t.Fatalf("second reservation returned the port still bound (%d)", port)
	}
}

func TestBrowserLaunchArgs(t *testing.T) {
	args := browserLaunchArgs("/profiles/glorp", 4321, false)
	want := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir=/profiles/glorp",
		"--remote-debugging-port=4321",
	}
	if len(args) != len(want) {
		t.Fatalf("args %v, want %v", args, want)
	}
	for i, arg := range want {
		if args[i] != arg {
			t.Fatalf("arg %d is %q, want %q", i, args[i], arg)
		}
	}
}

func TestBrowserDebugURLIsLoopbackOnly(t *testing.T) {
	// The DevTools endpoint is unauthenticated, so it must never be reachable
	// from outside the machine.
	if got := browserDebugURL(9222); got != "http://127.0.0.1:9222" {
		t.Fatalf("debug URL %q", got)
	}
}

// TestWaitForBrowserPollsUntilReady checks the readiness wait keeps polling
// while the browser is still starting, instead of giving up on the first
// refusal.
func TestWaitForBrowserPollsUntilReady(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Errorf("unexpected request for %s", r.URL.Path)
		}
		requests++
		if requests < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`)
	}))
	defer server.Close()

	wsURL, err := waitForBrowser(context.Background(), server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("waitForBrowser: %v", err)
	}
	if wsURL != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("WebSocket URL %q", wsURL)
	}
	if requests < 3 {
		t.Fatalf("endpoint was polled %d times, expected it to keep retrying", requests)
	}
}

func TestWaitForBrowserTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := waitForBrowser(context.Background(), server.URL, 250*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout when the browser never becomes ready")
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error %q does not name the endpoint", err)
	}
}

func TestWaitForBrowserStopsWhenContextIsCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForBrowser(ctx, server.URL, time.Minute); err == nil {
		t.Fatal("expected the canceled context to end the wait")
	}
}

func TestBrowserWebSocketURLRejectsEmptyEndpointReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	if _, err := browserWebSocketURL(context.Background(), server.URL); err == nil {
		t.Fatal("expected an error when the endpoint reports no WebSocket URL")
	}
}
