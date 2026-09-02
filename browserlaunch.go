package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// browserConfig collects the overrides that decide which browser glorp drives
// and where it keeps that browser's state. Both are empty by default, which
// means "discover a Chromium-based browser" and "use glorp's own profile
// directory"; the -browser flags fill them in.
type browserConfig struct {
	// Binary is an explicit browser executable (--browser-binary).
	Binary string
	// Profile is an explicit profile directory (--browser-profile).
	Profile string
	// Headed launches a visible browser window instead of a headless one. It is
	// how `glorp auth` puts a login form in front of the user, on the same
	// profile the headless watch loop reads GitHub with.
	Headed bool
}

const (
	// browserProfileName is the directory, under glorp's own configuration
	// directory, that holds the browser profile glorp owns. It is deliberately
	// separate from the user's everyday browser profile: glorp logs a browser
	// into GitHub and drives it unattended, which has no business touching the
	// profile the user browses with.
	browserProfileName = "browser-data"
	// browserReadyTimeout bounds the wait for a freshly launched browser to
	// start serving the DevTools endpoint. A cold profile on a slow machine
	// takes several seconds; anything past this is a failed launch.
	browserReadyTimeout = 30 * time.Second
	// browserReadyInterval is how often the DevTools endpoint is polled while
	// waiting for the browser to come up.
	browserReadyInterval = 100 * time.Millisecond
	// browserTargetTimeout bounds the wait for the window a headed browser
	// opens by itself to show up in the target list. The browser is already
	// answering the DevTools endpoint by the time it is asked, so the window is
	// normally there on the first read; past this it is not coming.
	browserTargetTimeout = 2 * time.Second
	// browserCommandTimeout bounds a single command sent to a tab: a
	// navigation, a reload, a script evaluation, a screenshot. GitHub pages
	// are large and a run paces its loads, so this is deliberately generous;
	// what it exists to catch is a page or a renderer that has stopped
	// answering altogether, which would otherwise park the poll loop for the
	// rest of the run (issue #472).
	browserCommandTimeout = 2 * time.Minute
)

// browserBinaryNames are the Chromium-based executables looked for on PATH, in
// preference order. Edge is included because it is Chromium underneath and so
// speaks the same DevTools Protocol; Safari has no CDP and cannot be used.
var browserBinaryNames = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"msedge",
}

// These indirections let the tests exercise discovery, profile paths, and the
// readiness wait without a browser installed and without launching one.
var (
	lookBrowserPath   = exec.LookPath
	statBrowserBinary = func(path string) error { _, err := os.Stat(path); return err }
	browserConfigDir  = os.UserConfigDir
)

// browserProfileDir reports the profile directory the browser is launched
// against: the override when one was given, otherwise glorp's own directory
// under the platform configuration directory (`~/Library/Application
// Support/glorp/browser-data` on macOS, `%AppData%\glorp\browser-data` on
// Windows, `~/.config/glorp/browser-data` on Linux).
func browserProfileDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	dir, err := browserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate browser profile directory: %w", err)
	}
	return filepath.Join(dir, "glorp", browserProfileName), nil
}

// findBrowserBinary returns the browser executable to launch. An override is
// resolved through PATH so a bare command name works as well as a full path,
// and a bad one is reported instead of being silently replaced by a discovered
// browser: a user who named a browser meant that browser.
func findBrowserBinary(override string) (string, error) {
	if override != "" {
		path, err := lookBrowserPath(override)
		if err != nil {
			return "", fmt.Errorf("browser binary %q not found or not executable: %w", override, err)
		}
		return path, nil
	}
	for _, name := range browserBinaryNames {
		if path, err := lookBrowserPath(name); err == nil {
			return path, nil
		}
	}
	for _, path := range browserBinaryPaths() {
		if path == "" {
			continue
		}
		if err := statBrowserBinary(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Chrome, Chromium or Edge installation found: install Google Chrome, or pass --browser-binary with the path to a Chromium-based browser (Safari cannot be used, it has no DevTools Protocol)")
}

// freeBrowserPort reserves a port for the browser's DevTools endpoint by
// letting the OS assign one and closing the listener again. Several glorp
// instances can watch at once, so a fixed port would have them collide on the
// endpoint and drive each other's browser.
func freeBrowserPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve browser debugging port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("reserve browser debugging port: %w", err)
	}
	return port, nil
}

// browserLaunchArgs are the flags glorp launches the browser with. The new
// headless mode is asked for by name because it is the one that behaves like a
// real browser; the first-run and default-browser prompts would otherwise block
// a fresh profile from ever reaching the DevTools endpoint. A headed launch
// drops the headless flags so a window actually appears, and keeps everything
// else so it lands on the same profile and DevTools endpoint.
func browserLaunchArgs(profile string, port int, headed bool) []string {
	args := make([]string, 0, 6)
	if !headed {
		args = append(args, "--headless=new", "--disable-gpu")
	}
	return append(args,
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+profile,
		"--remote-debugging-port="+strconv.Itoa(port),
	)
}

// browserDebugURL is the base URL of the DevTools endpoint on a given port.
func browserDebugURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// browserProcess is a running browser and the DevTools endpoint it answers on.
type browserProcess struct {
	cmd   *exec.Cmd
	port  int
	wsURL string
	// profile is the user-data directory the browser was launched against, so
	// a page that turns out to have been read by a signed-out session can name
	// the directory to sign in.
	profile string
}

// launchBrowser starts a browser against glorp's own profile and waits
// for its DevTools endpoint to answer. The browser is started as a tracked
// child process, so glorp's existing orphan guards take it down on every exit
// path, including one that runs no cleanup of its own.
func launchBrowser(ctx context.Context, config browserConfig) (*browserProcess, error) {
	binary, err := findBrowserBinary(config.Binary)
	if err != nil {
		return nil, err
	}
	profile, err := browserProfileDir(config.Profile)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, fmt.Errorf("create browser profile directory %s: %w", profile, err)
	}
	port, err := freeBrowserPort()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, browserLaunchArgs(profile, port, config.Headed)...)
	// The browser writes a steady stream of diagnostics to standard error that
	// would otherwise be painted over glorp's terminal UI.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := startChildProcess(cmd); err != nil {
		return nil, fmt.Errorf("start browser (%s): %w", binary, err)
	}
	wsURL, err := waitForBrowser(ctx, browserDebugURL(port), browserReadyTimeout)
	if err != nil {
		_ = stopChildProcess(cmd)
		return nil, err
	}
	return &browserProcess{cmd: cmd, port: port, wsURL: wsURL, profile: profile}, nil
}

// browserVersionInfo is the part of the DevTools /json/version response glorp
// reads: the browser-level WebSocket to drive the browser over.
type browserVersionInfo struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// waitForBrowser polls the DevTools endpoint until the browser answers with the
// WebSocket to connect on, or the timeout expires. The endpoint refuses
// connections until the browser is genuinely ready, so polling it is the
// readiness check rather than a guess at a startup delay.
func waitForBrowser(ctx context.Context, baseURL string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(browserReadyInterval)
	defer ticker.Stop()
	for {
		if wsURL, err := browserWebSocketURL(ctx, baseURL); err == nil {
			return wsURL, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for browser at %s: %w", baseURL, ctx.Err())
		case <-ticker.C:
		}
	}
}

// browserWebSocketURL asks the DevTools endpoint for the browser-level
// WebSocket URL.
func browserWebSocketURL(ctx context.Context, baseURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/version", nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("browser DevTools endpoint returned %s", response.Status)
	}
	var info browserVersionInfo
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("browser DevTools endpoint reported no WebSocket URL")
	}
	return info.WebSocketDebuggerURL, nil
}

// browserTargetInfo is the part of a DevTools /json/list entry glorp reads: a
// target's id and what kind of target it is.
type browserTargetInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// browserOpenPageTarget returns the id of a page the browser already has open.
// A headed browser puts a window on screen before glorp ever connects -- Chrome
// always opens one -- so the auth flow drives that window rather than asking
// for a new target, which would leave an empty "New Tab" window sitting beside
// the login window (issue #412).
//
// The target list is polled rather than read once because the initial window is
// created alongside the DevTools endpoint, not before it, and an empty list is
// reported as an error so the caller can fall back to opening its own tab.
func browserOpenPageTarget(ctx context.Context, baseURL string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(browserReadyInterval)
	defer ticker.Stop()
	for {
		if id, err := browserPageTargetID(ctx, baseURL); err == nil && id != "" {
			return id, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("no open browser window at %s: %w", baseURL, ctx.Err())
		case <-ticker.C:
		}
	}
}

// browserPageTargetID asks the DevTools endpoint for its targets and returns
// the first page among them. Anything that is not a page -- the DevTools
// front-end itself, service workers, extension backgrounds -- is skipped: they
// are not windows and cannot be navigated to GitHub.
func browserPageTargetID(ctx context.Context, baseURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/list", nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("browser DevTools endpoint returned %s", response.Status)
	}
	var targets []browserTargetInfo
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return "", err
	}
	for _, info := range targets {
		if info.Type == "page" && info.ID != "" {
			return info.ID, nil
		}
	}
	return "", fmt.Errorf("browser DevTools endpoint reported no open page")
}
