package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/lsegal/glorp/core"
)

// newRestartHandler backs the web dashboard's restart button (issue #665) and
// the updater's restart (issue #783), logging reason when it starts. It
// records that a restart was asked for, takes the shutdown checkpoint that
// pushes every active run's work (issue #741), and only then starts the same
// shutdown Ctrl+C does, reporting each run's checkpoint outcome. runWatch
// relaunches glorp once that shutdown has finished and every deferred cleanup
// has released the ports, browser, and subprocesses the new instance needs. A
// second press while the first is still shutting down is refused rather than
// queued.
func newRestartHandler(requested *atomic.Bool, checkpoint func() []core.CheckpointResult, shutdown func(), logf func(string, ...interface{}), reason string) func(context.Context) ([]core.CheckpointResult, error) {
	return func(context.Context) ([]core.CheckpointResult, error) {
		if !requested.CompareAndSwap(false, true) {
			return nil, fmt.Errorf("glorp is already restarting")
		}
		logf("%s; pushing active work, then stopping glorp and starting it again", reason)
		results := checkpoint()
		shutdown()
		return results, nil
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
