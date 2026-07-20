//go:build !production

package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestWebUIFrontendStartsViteInDevelopment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := startWebUIFrontend(ctx, io.Discard)
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
