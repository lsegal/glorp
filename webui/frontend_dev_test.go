//go:build !production

package webui

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// viteStartTimeout bounds how long the test waits for Vite to answer. A cold
// start on a slow CI runner, notably Windows, can take well over ten seconds.
const viteStartTimeout = 2 * time.Minute

// execSupervisor runs the dev server directly, standing in for glorp's tracked
// child-process helpers, which live in the root package. It reports when the
// started process exits, so the test can stop waiting on a server that died.
type execSupervisor struct {
	exited chan struct{}
	err    error
}

func newExecSupervisor() *execSupervisor {
	return &execSupervisor{exited: make(chan struct{})}
}

func (s *execSupervisor) Start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		s.err = cmd.Wait()
		close(s.exited)
	}()
	return nil
}

func (*execSupervisor) Run(cmd *exec.Cmd) error { return cmd.Run() }

func (s *execSupervisor) Stop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	<-s.exited
	return nil
}

// syncBuffer collects the dev server's output from its stdout and stderr.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFrontendStartsViteInDevelopment(t *testing.T) {
	// A development build runs from the repository root and resolves the Vite
	// project at FrontendDir relative to it, but `go test` runs this package
	// from its own directory, so step back up before starting the frontend.
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(".."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(root) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &syncBuffer{}
	supervisor := newExecSupervisor()
	stop, err := StartFrontend(ctx, output, supervisor)
	if err != nil {
		t.Fatalf("start frontend: %v\n%s", err, output)
	}
	defer stop()

	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.After(viteStartTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(viteDevURL)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-supervisor.exited:
			t.Fatalf("Vite exited before answering at %s: %v\n%s", viteDevURL, supervisor.err, output)
		case <-deadline:
			t.Fatalf("Vite did not answer at %s within %s\n%s", viteDevURL, viteStartTimeout, output)
		case <-ticker.C:
		}
	}
}
