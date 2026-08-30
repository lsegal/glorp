package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAuthPage stands in for the tab `glorp auth` drives: it records where it
// was sent and answers each sign-in read with the next canned login, so the
// flow can be exercised without a browser and without a window.
type fakeAuthPage struct {
	mu        sync.Mutex
	logins    []string
	evalErrs  []error
	navErr    error
	navigated []string
	evals     int
}

func (p *fakeAuthPage) Navigate(url string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.navigated = append(p.navigated, url)
	return p.navErr
}

func (p *fakeAuthPage) Reload() error { return nil }

func (p *fakeAuthPage) HTTPStatus() int { return 200 }

func (p *fakeAuthPage) Eval(_ string, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.evals
	p.evals++
	if at < len(p.evalErrs) && p.evalErrs[at] != nil {
		return p.evalErrs[at]
	}
	login := ""
	if at < len(p.logins) {
		login = p.logins[at]
	} else if len(p.logins) > 0 {
		// Later reads keep answering with the final canned value, so a poll
		// loop that overshoots does not fall off the end of the script.
		login = p.logins[len(p.logins)-1]
	}
	target, ok := out.(*string)
	if !ok {
		return fmt.Errorf("sign-in read decoded into %T, want *string", out)
	}
	*target = login
	return nil
}

// withAuthSession swaps in a session backed by the given page for one test.
func withAuthSession(t *testing.T, page *fakeAuthPage, err error) *[]browserConfig {
	t.Helper()
	var configs []browserConfig
	original := openAuthSession
	openAuthSession = func(_ context.Context, config browserConfig) (*authSession, error) {
		configs = append(configs, config)
		if err != nil {
			return nil, err
		}
		return &authSession{page: page, close: func() {}}, nil
	}
	t.Cleanup(func() { openAuthSession = original })
	return &configs
}

// fastAuthPolling shrinks the sign-in wait's poll interval so a test exercising
// the loop does not spend real seconds in it.
func fastAuthPolling(t *testing.T) {
	t.Helper()
	original := authPollInterval
	authPollInterval = time.Millisecond
	t.Cleanup(func() { authPollInterval = original })
}

func TestAuthCommandIsRegistered(t *testing.T) {
	cmd, ok := lookupCommand("auth")
	if !ok {
		t.Fatal("no auth command registered; `glorp auth` and `glorp help auth` do not work")
	}
	if cmd.run == nil {
		t.Fatal("auth command has no run function")
	}
	if !strings.Contains(cmd.usage, "glorp auth") {
		t.Fatalf("auth usage does not name the command: %q", cmd.usage)
	}
	flags := commandFlags("auth")
	if flags == nil {
		t.Fatal("commandFlags(\"auth\") is nil, so `glorp help auth` prints no flags")
	}
	for _, name := range []string{"status", "browser-profile", "browser-binary"} {
		if flags.Lookup(name) == nil {
			t.Errorf("auth flag set has no -%s", name)
		}
	}
}

func TestAuthCommandListedInUsage(t *testing.T) {
	var out strings.Builder
	printUsage(&out)
	if !strings.Contains(out.String(), "auth") {
		t.Fatalf("top-level usage does not list auth:\n%s", out.String())
	}
}

func TestReadGitHubLoginReportsSignedOutAsEmpty(t *testing.T) {
	page := &fakeAuthPage{logins: []string{""}}
	login, err := readGitHubLogin(page)
	if err != nil {
		t.Fatalf("readGitHubLogin: %v", err)
	}
	if login != "" {
		t.Fatalf("login %q, want empty for a signed-out profile", login)
	}
}

func TestReadGitHubLoginTrimsAccountName(t *testing.T) {
	page := &fakeAuthPage{logins: []string{"  octocat\n"}}
	login, err := readGitHubLogin(page)
	if err != nil {
		t.Fatalf("readGitHubLogin: %v", err)
	}
	if login != "octocat" {
		t.Fatalf("login %q, want octocat", login)
	}
}

func TestAuthStatusSignedIn(t *testing.T) {
	page := &fakeAuthPage{logins: []string{"octocat"}}
	configs := withAuthSession(t, page, nil)
	var out strings.Builder
	signedIn, err := authStatus(context.Background(), browserConfig{Profile: "/profiles/glorp"}, &out)
	if err != nil {
		t.Fatalf("authStatus: %v", err)
	}
	if !signedIn {
		t.Fatal("signedIn is false for a profile reporting a login")
	}
	if !strings.Contains(out.String(), "octocat") {
		t.Fatalf("status output does not name the account: %q", out.String())
	}
	if !strings.Contains(out.String(), "/profiles/glorp") {
		t.Fatalf("status output does not name the profile: %q", out.String())
	}
	if len(*configs) != 1 || (*configs)[0].Headed {
		t.Fatalf("status opened %+v, want exactly one headless session", *configs)
	}
	if len(page.navigated) != 1 || page.navigated[0] != githubHomeURL {
		t.Fatalf("navigated to %v, want [%s]", page.navigated, githubHomeURL)
	}
}

func TestAuthStatusSignedOut(t *testing.T) {
	withAuthSession(t, &fakeAuthPage{logins: []string{""}}, nil)
	var out strings.Builder
	signedIn, err := authStatus(context.Background(), browserConfig{}, &out)
	if err != nil {
		t.Fatalf("authStatus: %v", err)
	}
	if signedIn {
		t.Fatal("signedIn is true for a profile with no login")
	}
	if !strings.Contains(out.String(), "Not signed in") {
		t.Fatalf("status output %q does not report a signed-out profile", out.String())
	}
}

func TestAuthStatusReportsLaunchFailure(t *testing.T) {
	withAuthSession(t, nil, errors.New("no Chrome installed"))
	var out strings.Builder
	if _, err := authStatus(context.Background(), browserConfig{}, &out); err == nil {
		t.Fatal("authStatus succeeded with no browser to launch")
	}
}

func TestAuthLoginOpensHeadedWindowAtLoginPage(t *testing.T) {
	page := &fakeAuthPage{logins: []string{"octocat"}}
	configs := withAuthSession(t, page, nil)
	var out strings.Builder
	login, err := authLogin(context.Background(), browserConfig{Profile: "/profiles/glorp"}, &out, time.Second)
	if err != nil {
		t.Fatalf("authLogin: %v", err)
	}
	if login != "octocat" {
		t.Fatalf("login %q, want octocat", login)
	}
	if len(*configs) != 1 || !(*configs)[0].Headed {
		t.Fatalf("login opened %+v, want exactly one headed session", *configs)
	}
	if len(page.navigated) != 1 || page.navigated[0] != githubLoginURL {
		t.Fatalf("navigated to %v, want [%s]", page.navigated, githubLoginURL)
	}
	// An already-signed-in profile is reported without telling the user to go
	// and sign in.
	if strings.Contains(out.String(), "sign in to GitHub there") {
		t.Fatalf("prompted for a sign-in that was not needed: %q", out.String())
	}
}

func TestWaitForGitHubLoginReturnsOnceSignedIn(t *testing.T) {
	fastAuthPolling(t)
	page := &fakeAuthPage{logins: []string{"", "", "octocat"}}
	login, err := waitForGitHubLogin(context.Background(), page, 5*time.Second)
	if err != nil {
		t.Fatalf("waitForGitHubLogin: %v", err)
	}
	if login != "octocat" {
		t.Fatalf("login %q, want octocat", login)
	}
}

func TestWaitForGitHubLoginTimesOut(t *testing.T) {
	fastAuthPolling(t)
	page := &fakeAuthPage{logins: []string{""}}
	_, err := waitForGitHubLogin(context.Background(), page, 20*time.Millisecond)
	if err == nil {
		t.Fatal("waitForGitHubLogin succeeded on a profile that never signed in")
	}
	if !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("error %q does not report giving up on the wait", err)
	}
}

func TestWaitForGitHubLoginToleratesReadsDuringNavigation(t *testing.T) {
	fastAuthPolling(t)
	// A read that lands while the window is navigating fails; the wait keeps
	// polling rather than treating it as a failed sign-in.
	page := &fakeAuthPage{
		logins:   []string{"", "", "octocat"},
		evalErrs: []error{nil, errors.New("context canceled"), nil},
	}
	login, err := waitForGitHubLogin(context.Background(), page, 5*time.Second)
	if err != nil {
		t.Fatalf("waitForGitHubLogin: %v", err)
	}
	if login != "octocat" {
		t.Fatalf("login %q, want octocat", login)
	}
}

func TestWaitForGitHubLoginReportsLastReadError(t *testing.T) {
	fastAuthPolling(t)
	page := &fakeAuthPage{evalErrs: []error{errors.New("tab closed"), errors.New("tab closed")}}
	_, err := waitForGitHubLogin(context.Background(), page, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "tab closed") {
		t.Fatalf("error %v, want the last failed read included", err)
	}
}

func TestWaitForGitHubLoginStopsWithTheContext(t *testing.T) {
	fastAuthPolling(t)
	page := &fakeAuthPage{logins: []string{""}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForGitHubLogin(ctx, page, time.Hour); err == nil {
		t.Fatal("waitForGitHubLogin ignored a cancelled context")
	}
}

func TestCheckHeadedEnvironment(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	for _, test := range []struct {
		name    string
		goos    string
		values  map[string]string
		wantErr bool
	}{
		{name: "macOS always has a window server", goos: "darwin"},
		{name: "Windows always has a desktop", goos: "windows"},
		{name: "Linux with X11", goos: "linux", values: map[string]string{"DISPLAY": ":0"}},
		{name: "Linux with Wayland", goos: "linux", values: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}},
		{name: "Linux with no display server", goos: "linux", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkHeadedEnvironment(test.goos, env(test.values))
			if test.wantErr {
				if err == nil {
					t.Fatal("opened a sign-in window with no display server")
				}
				if !strings.Contains(err.Error(), "glorp auth") {
					t.Fatalf("error %q does not say how to sign in instead", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkHeadedEnvironment: %v", err)
			}
		})
	}
}

func TestRunAuthCommandRejectsExtraArguments(t *testing.T) {
	withAuthSession(t, &fakeAuthPage{logins: []string{"octocat"}}, nil)
	if code := runAuthCommand([]string{"extra"}); code != 2 {
		t.Fatalf("exit code %d, want 2 for an unexpected argument", code)
	}
}

func TestRunAuthCommandStatusExitCodes(t *testing.T) {
	withAuthSession(t, &fakeAuthPage{logins: []string{"octocat"}}, nil)
	if code := runAuthCommand([]string{"-status"}); code != 0 {
		t.Fatalf("exit code %d, want 0 for a signed-in profile", code)
	}
	withAuthSession(t, &fakeAuthPage{logins: []string{""}}, nil)
	if code := runAuthCommand([]string{"-status"}); code != 1 {
		t.Fatalf("exit code %d, want 1 for a signed-out profile", code)
	}
}

func TestRunAuthCommandPassesBrowserFlagsThrough(t *testing.T) {
	configs := withAuthSession(t, &fakeAuthPage{logins: []string{"octocat"}}, nil)
	if code := runAuthCommand([]string{"-status", "-browser-profile", "/tmp/p", "-browser-binary", "/tmp/chrome"}); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if len(*configs) != 1 {
		t.Fatalf("opened %d sessions, want 1", len(*configs))
	}
	if got := (*configs)[0]; got.Profile != "/tmp/p" || got.Binary != "/tmp/chrome" {
		t.Fatalf("config %+v, want the -browser-profile and -browser-binary values", got)
	}
}

func TestBrowserLaunchArgsHeaded(t *testing.T) {
	args := browserLaunchArgs("/profiles/glorp", 4321, true)
	for _, unwanted := range []string{"--headless=new", "--disable-gpu"} {
		for _, arg := range args {
			if arg == unwanted {
				t.Fatalf("headed launch still passes %s: %v", unwanted, args)
			}
		}
	}
	want := map[string]bool{
		"--no-first-run":                  true,
		"--no-default-browser-check":      true,
		"--user-data-dir=/profiles/glorp": true,
		"--remote-debugging-port=4321":    true,
	}
	for _, arg := range args {
		delete(want, arg)
	}
	if len(want) != 0 {
		t.Fatalf("headed launch is missing %v: %v", want, args)
	}
}
