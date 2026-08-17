package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// childProcesses tracks every subprocess glorp owns so none of them can outlive
// the parent (issue #260). Helpers such as the ngrok tunnel keep running by
// themselves once started, so glorp terminates them on every exit path it can
// observe: a normal return, a fatal os.Exit, a signal, or a panic.
var childProcesses = &processTracker{}

// processSignal is the portable subset of process signalling glorp needs.
type processSignal int

const (
	// checkSignal asks whether the process tree is still running without
	// disturbing it.
	checkSignal processSignal = iota
	// terminateSignal asks the process tree to shut down cleanly.
	terminateSignal
	// killSignal ends the process tree unconditionally.
	killSignal
)

const (
	// stopGrace is how long a child gets to exit after being asked politely,
	// before it is killed outright.
	stopGrace = 2 * time.Second
	// reapGrace is the shorter budget used while glorp itself is exiting, so
	// shutting down stays responsive.
	reapGrace = 500 * time.Millisecond
)

type processTracker struct {
	mu      sync.Mutex
	running map[*exec.Cmd]struct{}
}

func (t *processTracker) track(cmd *exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running == nil {
		t.running = map[*exec.Cmd]struct{}{}
	}
	t.running[cmd] = struct{}{}
}

func (t *processTracker) forget(cmd *exec.Cmd) {
	t.mu.Lock()
	delete(t.running, cmd)
	t.mu.Unlock()
	// A child glorp no longer owns must stop being a reap target, or a platform
	// guard that reaps recorded process groups could signal a reused id.
	releaseOrphanedProcess(cmd)
}

func (t *processTracker) commands() []*exec.Cmd {
	t.mu.Lock()
	defer t.mu.Unlock()
	commands := make([]*exec.Cmd, 0, len(t.running))
	for cmd := range t.running {
		commands = append(commands, cmd)
	}
	return commands
}

// reap terminates every tracked process tree. It only signals the processes and
// never calls Wait, because the goroutine that started a child owns that call
// and reaping must not race it.
func (t *processTracker) reap() {
	commands := t.commands()
	for _, cmd := range commands {
		_ = signalProcessTree(cmd, terminateSignal)
	}
	deadline := time.Now().Add(reapGrace)
	for len(commands) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		alive := commands[:0]
		for _, cmd := range commands {
			if signalProcessTree(cmd, checkSignal) == nil {
				alive = append(alive, cmd)
			}
		}
		commands = alive
	}
	for _, cmd := range commands {
		_ = signalProcessTree(cmd, killSignal)
	}
	for _, cmd := range t.commands() {
		t.forget(cmd)
	}
}

// spawner starts every owned subprocess from one pinned OS thread. See
// processSpawner for why that matters.
var spawner = &processSpawner{}

// processSpawner forks owned subprocesses from a single OS thread that lives as
// long as glorp does. Linux's parent-death signal (issue #264) is delivered when
// the *thread* that forked the child exits, not when the process does, and Go's
// runtime retires idle threads whenever it likes. Forking from an ordinary
// goroutine would therefore kill healthy children at unpredictable moments, so
// all spawning is funnelled through one goroutine that locks its thread and
// never gives it back.
type processSpawner struct {
	once     sync.Once
	requests chan spawnRequest
}

type spawnRequest struct {
	cmd    *exec.Cmd
	result chan<- error
}

// start runs cmd.Start on the pinned spawn thread and returns its error.
func (s *processSpawner) start(cmd *exec.Cmd) error {
	s.once.Do(func() {
		s.requests = make(chan spawnRequest)
		ready := make(chan struct{})
		go func() {
			// Never unlocked and never returned from: the thread must outlive
			// every child forked on it.
			runtime.LockOSThread()
			close(ready)
			for request := range s.requests {
				request.result <- request.cmd.Start()
			}
		}()
		<-ready
	})
	result := make(chan error, 1)
	s.requests <- spawnRequest{cmd: cmd, result: result}
	return <-result
}

// startChildProcess starts cmd as an owned subprocess: it runs in its own
// process group so the processes it spawns are terminated with it, it is
// tracked so glorp kills the whole tree before exiting, and the kernel is asked
// to tear it down should glorp die without running any cleanup at all.
//
// On platforms where the kernel guard can only be installed on a process that
// already exists, the child is created in a state where it has not run yet and
// adoptOrphanedProcess releases it; a child that cannot be released is killed
// rather than handed back to the caller suspended forever.
func startChildProcess(cmd *exec.Cmd) error {
	isolateProcessTree(cmd)
	guardOrphanedProcess(cmd)
	if err := spawner.start(cmd); err != nil {
		return err
	}
	childProcesses.track(cmd)
	if err := adoptOrphanedProcess(cmd); err != nil {
		_ = signalProcessTree(cmd, killSignal)
		_ = waitChildProcess(cmd)
		return err
	}
	return nil
}

// waitChildProcess waits for a tracked child and stops tracking it.
func waitChildProcess(cmd *exec.Cmd) error {
	err := cmd.Wait()
	childProcesses.forget(cmd)
	return err
}

// runChildProcess runs a tracked child to completion.
func runChildProcess(cmd *exec.Cmd) error {
	if err := startChildProcess(cmd); err != nil {
		return err
	}
	return waitChildProcess(cmd)
}

// outputChildProcess runs a tracked child and returns what it wrote to standard
// output, the tracked equivalent of exec.Cmd.Output.
func outputChildProcess(cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := runChildProcess(cmd)
	return stdout.Bytes(), err
}

// combinedOutputChildProcess runs a tracked child and returns its standard
// output and standard error interleaved, the tracked equivalent of
// exec.Cmd.CombinedOutput.
func combinedOutputChildProcess(cmd *exec.Cmd) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := runChildProcess(cmd)
	return output.Bytes(), err
}

// stopChildProcess terminates a tracked child's process tree and reaps it,
// asking politely first so the child can shut itself down cleanly.
func stopChildProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = signalProcessTree(cmd, terminateSignal)
	exited := make(chan error, 1)
	go func() { exited <- waitChildProcess(cmd) }()
	select {
	case err := <-exited:
		return stopError(err)
	case <-time.After(stopGrace):
	}
	_ = signalProcessTree(cmd, killSignal)
	return stopError(<-exited)
}

// stopError discards the failure a child reports when it dies from the signal
// glorp just sent it, which is the expected outcome of stopping it.
func stopError(err error) error {
	var exit *exec.ExitError
	if err == nil || errors.Is(err, os.ErrProcessDone) || errors.As(err, &exit) {
		return nil
	}
	return err
}

// reapChildProcesses terminates every owned subprocess. It is safe to call more
// than once, so shutdown paths can call it defensively.
func reapChildProcesses() { childProcesses.reap() }

// exitAfterReaping terminates every owned subprocess before exiting, because
// os.Exit runs no deferred cleanup.
func exitAfterReaping(code int) {
	reapChildProcesses()
	os.Exit(code)
}

// shutdownSignals are the signals that mean glorp should stop. Hangup is
// included so closing the terminal that started glorp takes its subprocesses
// down instead of leaving them behind.
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// shutdownContext returns a context that is canceled by the first shutdown
// signal, giving glorp a chance to stop its subprocesses in order. A second
// signal means "now": every owned subprocess is killed and glorp exits
// immediately, so an impatient second Ctrl-C cannot leave an orphan behind.
func shutdownContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, len(shutdownSignals))
	signal.Notify(signals, shutdownSignals...)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
			return
		}
		<-signals
		exitAfterReaping(1)
	}()
	return ctx, func() {
		signal.Stop(signals)
		cancel()
	}
}
