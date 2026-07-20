//go:build !production

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
)

const viteDevURL = "http://127.0.0.1:5173"

func newWebUIAssets() http.Handler {
	frontend, _ := url.Parse(viteDevURL)
	return httputil.NewSingleHostReverseProxy(frontend)
}

func startWebUIFrontend(ctx context.Context, output io.Writer) (func(), error) {
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
