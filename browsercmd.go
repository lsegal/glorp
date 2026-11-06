package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lsegal/glorp/browser"
)

// openBrowserWindow opens the window and blocks until it closes. It is a
// variable so tests can exercise the command without opening one.
var openBrowserWindow = browser.OpenWindow

func browserFlagSet() *flag.FlagSet {
	flags := flag.NewFlagSet("browser", flag.ExitOnError)
	flags.String("browser-profile", "", "Chrome profile directory to open (default: <config dir>/glorp/browser-data)")
	flags.String("browser-binary", "", "Chrome/Chromium/Edge executable to open it with")
	return flags
}

// browserWindow opens a visible browser on the configured profile with nothing
// attached to it and waits for the user to close it.
func browserWindow(ctx context.Context, config browser.Config, out io.Writer) error {
	if headedEnvironmentCheck() != nil {
		return errors.New("cannot open a browser window: no display server (DISPLAY and WAYLAND_DISPLAY are both unset). Run `glorp browser` from a desktop session")
	}
	config.Headed = true
	fmt.Fprintf(out, "Opening a browser window on %s. Close it when you are done; your changes stay on the profile.\n", describeAuthProfile(config.Profile))
	return openBrowserWindow(ctx, config)
}

func runBrowserCommand(args []string) int {
	flags := browserFlagSet()
	flags.Usage = func() {
		cmd, _ := lookupCommand("browser")
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
	config := browser.Config{
		Binary:  flagValue[string](flags, "browser-binary"),
		Profile: flagValue[string](flags, "browser-profile"),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := browserWindow(ctx, config, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
