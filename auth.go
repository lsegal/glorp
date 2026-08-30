package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	// githubHomeURL is loaded to read a profile's sign-in state. Every page
	// GitHub renders for a signed-in session carries the account's login, so
	// the home page answers the question without an API call or a token.
	githubHomeURL = "https://github.com/"
	// githubLoginURL is where the headed window starts, so the user lands on
	// the sign-in form instead of having to find it.
	githubLoginURL = "https://github.com/login"
	// authLoginTimeout bounds the wait for a sign-in to finish. Past it the
	// command gives up rather than holding a browser window open forever.
	authLoginTimeout = 5 * time.Minute
)

// authPollInterval is how often the open window is re-read while waiting for
// the sign-in to complete. It is a variable so tests can drive the wait loop
// without spending real seconds on it.
var authPollInterval = 2 * time.Second

// githubLoginJS reads the authenticated user's login out of the current page.
// GitHub serves `<meta name="user-login">` on every page it renders for a
// signed-in session and leaves it empty when signed out, so the one read is
// both the sign-in marker and the account name.
const githubLoginJS = `(() => {
  const meta = document.querySelector('meta[name="user-login"]');
  return meta ? (meta.getAttribute('content') || '') : '';
})()`

// authSession is a browser opened on glorp's profile, the page to drive it
// through, and the closer that stops the process again.
type authSession struct {
	page  browserPage
	close func()
}

// openAuthSession launches a browser on the configured profile and opens the
// tab the auth flow drives. It is a variable so tests can exercise the flow
// without a browser installed and without opening a window.
var openAuthSession = func(ctx context.Context, config browserConfig) (*authSession, error) {
	browser, err := startBrowser(ctx, config)
	if err != nil {
		return nil, err
	}
	page, err := browser.Tab("auth")
	if err != nil {
		_ = browser.Close()
		return nil, err
	}
	return &authSession{page: page, close: func() { _ = browser.Close() }}, nil
}

// readGitHubLogin reports the login the current page is rendered for, or an
// empty string when the profile is signed out.
func readGitHubLogin(page browserPage) (string, error) {
	var login string
	if err := page.Eval(githubLoginJS, &login); err != nil {
		return "", fmt.Errorf("read GitHub sign-in state: %w", err)
	}
	return strings.TrimSpace(login), nil
}

// visitGitHubLogin navigates to a GitHub URL and reads the sign-in state off
// the page it lands on.
func visitGitHubLogin(page browserPage, url string) (string, error) {
	if err := page.Navigate(url); err != nil {
		return "", fmt.Errorf("open %s: %w", url, err)
	}
	return readGitHubLogin(page)
}

// describeAuthProfile names the profile directory being signed in, so the user
// can tell which profile a status line is about when -browser-profile is used.
func describeAuthProfile(override string) string {
	dir, err := browserProfileDir(override)
	if err != nil {
		return "glorp's browser profile"
	}
	return dir
}

// authStatus reports whether the configured profile is signed in to GitHub. It
// reads the state headlessly: answering the question must not put a window on
// the user's screen.
func authStatus(ctx context.Context, config browserConfig, out io.Writer) (bool, error) {
	config.Headed = false
	session, err := openAuthSession(ctx, config)
	if err != nil {
		return false, err
	}
	defer session.close()
	login, err := visitGitHubLogin(session.page, githubHomeURL)
	if err != nil {
		return false, err
	}
	if login == "" {
		fmt.Fprintf(out, "Not signed in to GitHub (%s); run `glorp auth` to sign in.\n", describeAuthProfile(config.Profile))
		return false, nil
	}
	fmt.Fprintf(out, "Signed in to GitHub as %s (%s).\n", login, describeAuthProfile(config.Profile))
	return true, nil
}

// authLogin opens a visible browser window on glorp's own profile at GitHub's
// login page and waits until the profile is signed in. Chrome allows a single
// process per --user-data-dir, so this cannot run while `glorp watch -browser`
// is driving the same profile; that is why the command exists as the manual,
// documented path rather than something watch does on the side.
func authLogin(ctx context.Context, config browserConfig, out io.Writer, timeout time.Duration) (string, error) {
	if err := checkHeadedEnvironment(runtime.GOOS, os.Getenv); err != nil {
		return "", err
	}
	config.Headed = true
	session, err := openAuthSession(ctx, config)
	if err != nil {
		return "", err
	}
	defer session.close()
	login, err := visitGitHubLogin(session.page, githubLoginURL)
	if err != nil {
		return "", err
	}
	if login != "" {
		return login, nil
	}
	fmt.Fprintf(out, "Opened a browser window on %s — sign in to GitHub there and glorp will close it.\n", describeAuthProfile(config.Profile))
	return waitForGitHubLogin(ctx, session.page, timeout)
}

// waitForGitHubLogin re-reads the open window until it reports a signed-in
// session or the deadline passes.
func waitForGitHubLogin(ctx context.Context, page browserPage, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(authPollInterval)
	defer ticker.Stop()
	var last error
	for {
		select {
		case <-ctx.Done():
			if last != nil {
				return "", fmt.Errorf("gave up after %s waiting for the GitHub sign-in to finish: %w", timeout, last)
			}
			return "", fmt.Errorf("gave up after %s waiting for the GitHub sign-in to finish", timeout)
		case <-ticker.C:
		}
		login, err := readGitHubLogin(page)
		if err != nil {
			// The window navigates repeatedly during a sign-in, so a read that
			// lands mid-navigation fails harmlessly. Only a read that never
			// succeeds before the deadline is worth reporting.
			last = err
			continue
		}
		if login != "" {
			return login, nil
		}
	}
}

// checkHeadedEnvironment refuses to open a login window where no window could
// appear. On Linux a session with no display server would leave the user
// waiting on a browser that never shows up; macOS and Windows always have one.
func checkHeadedEnvironment(goos string, getenv func(string) string) error {
	if goos != "linux" {
		return nil
	}
	if getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != "" {
		return nil
	}
	return fmt.Errorf("cannot open a sign-in window: no display server (DISPLAY and WAYLAND_DISPLAY are both unset). Run `glorp auth` from a desktop session, then point -browser-profile at the signed-in profile directory from here")
}

func authFlagSet() *flag.FlagSet {
	flags := flag.NewFlagSet("auth", flag.ExitOnError)
	flags.Bool("status", false, "report whether the profile is signed in and exit, without opening a window")
	flags.String("browser-profile", "", "Chrome profile directory to sign in (default: <config dir>/glorp/browser-data)")
	flags.String("browser-binary", "", "Chrome/Chromium/Edge executable to sign in with")
	return flags
}

func runAuthCommand(args []string) int {
	flags := authFlagSet()
	flags.Usage = func() {
		cmd, _ := lookupCommand("auth")
		fmt.Fprintln(os.Stderr, cmd.usage)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		flags.Usage()
		return 2
	}
	config := browserConfig{
		Binary:  flagValue[string](flags, "browser-binary"),
		Profile: flagValue[string](flags, "browser-profile"),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if flagValue[bool](flags, "status") {
		signedIn, err := authStatus(ctx, config, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if !signedIn {
			return 1
		}
		return 0
	}
	login, err := authLogin(ctx, config, os.Stdout, authLoginTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Signed in to GitHub as %s.\n", login)
	return 0
}
