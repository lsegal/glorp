//go:build !production

package main

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

func newWebUIAssets() http.Handler {
	frontend, _ := url.Parse(viteDevURL)
	return httputil.NewSingleHostReverseProxy(frontend)
}

func startWebUIFrontend(ctx context.Context, output io.Writer) (func(), error) {
	if err := ensureViteInstalled(ctx, output); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "node", "node_modules/vite/bin/vite.js", "--host", "127.0.0.1", "--port", "5173", "--strictPort")
	command.Dir = "web"
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return nil, err
	}
	return func() {
		if command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}, nil
}

func ensureViteInstalled(ctx context.Context, output io.Writer) error {
	vitePath := filepath.Join("web", "node_modules", "vite", "bin", "vite.js")
	if _, err := os.Stat(vitePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	command := exec.CommandContext(ctx, "pnpm", "install", "--frozen-lockfile")
	command.Dir = "web"
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("install web UI dependencies: %w", err)
	}
	return nil
}
