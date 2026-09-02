//go:build !production

package webui

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// execSupervisor runs the dev server directly, standing in for glorp's tracked
// child-process helpers, which live in the root package.
type execSupervisor struct{}

func (execSupervisor) Start(cmd *exec.Cmd) error { return cmd.Start() }

func (execSupervisor) Run(cmd *exec.Cmd) error { return cmd.Run() }

func (execSupervisor) Stop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil
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
	stop, err := StartFrontend(ctx, io.Discard, execSupervisor{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(viteDevURL)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Vite did not start at %s", viteDevURL)
}
