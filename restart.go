package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
)

// newRestartHandler backs the web dashboard's restart button (issue #665). It
// records that a restart was asked for and starts the same shutdown Ctrl+C
// does; runWatch relaunches glorp once that shutdown has finished and every
// deferred cleanup has released the ports, browser, and subprocesses the new
// instance needs. A second press while the first is still shutting down is
// refused rather than queued.
func newRestartHandler(requested *atomic.Bool, shutdown func(), logf func(string, ...interface{})) func(context.Context) error {
	return func(context.Context) error {
		if !requested.CompareAndSwap(false, true) {
			return fmt.Errorf("glorp is already restarting")
		}
		logf("restart requested from the web dashboard; stopping glorp and starting it again")
		shutdown()
		return nil
	}
}

// restartExecutable strips the " (deleted)" suffix Linux reports for a binary
// replaced on disk since it started, as `glorp upgrade` does, so a restart
// runs the upgraded binary at its original path.
func restartExecutable(executable string) string {
	return strings.TrimSuffix(executable, " (deleted)")
}

// relaunch starts glorp again with the command line and environment this run
// started with, reporting the exit code runWatch should return.
func relaunch(stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "restart glorp: %v\n", err)
		return 1
	}
	code, err := relaunchProcess(restartExecutable(executable), os.Args, os.Environ())
	if err != nil {
		fmt.Fprintf(stderr, "restart glorp: %v\n", err)
		return 1
	}
	return code
}
