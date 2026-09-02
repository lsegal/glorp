//go:build !production

package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
)

const viteDevURL = "http://127.0.0.1:5173"

func newAssets() http.Handler {
	frontend, _ := url.Parse(viteDevURL)
	return httputil.NewSingleHostReverseProxy(frontend)
}

// StartFrontend runs Vite against the frontend beside this package and reports
// a function that stops it.
func StartFrontend(ctx context.Context, output io.Writer, supervisor Supervisor) (func(), error) {
	if err := ensureViteInstalled(ctx, output, supervisor); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "node", "node_modules/vite/bin/vite.js", "--host", "127.0.0.1", "--port", "5173", "--strictPort")
	command.Dir = FrontendDir
	command.Stdout = output
	command.Stderr = output
	if err := supervisor.Start(command); err != nil {
		return nil, err
	}
	return func() { _ = supervisor.Stop(command) }, nil
}

func ensureViteInstalled(ctx context.Context, output io.Writer, supervisor Supervisor) error {
	vitePath := filepath.Join(FrontendDir, "node_modules", "vite", "bin", "vite.js")
	if _, err := os.Stat(vitePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	command := exec.CommandContext(ctx, "pnpm", "install", "--frozen-lockfile")
	command.Dir = FrontendDir
	command.Stdout = output
	command.Stderr = output
	if err := supervisor.Run(command); err != nil {
		return fmt.Errorf("install web UI dependencies: %w", err)
	}
	return nil
}
