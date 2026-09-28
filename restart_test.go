package main

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestRestartHandlerShutsDownOnce(t *testing.T) {
	var requested atomic.Bool
	shutdowns := 0
	handler := newRestartHandler(&requested, func() { shutdowns++ }, func(string, ...interface{}) {})
	if err := handler(context.Background()); err != nil {
		t.Fatalf("first restart: %v", err)
	}
	if !requested.Load() || shutdowns != 1 {
		t.Fatalf("after first restart: requested = %v, shutdowns = %d", requested.Load(), shutdowns)
	}
	if err := handler(context.Background()); err == nil {
		t.Fatal("second restart while shutting down succeeded, want an error")
	}
	if shutdowns != 1 {
		t.Fatalf("second restart shut down again: shutdowns = %d", shutdowns)
	}
}

func TestRestartExecutableUsesReplacedBinaryPath(t *testing.T) {
	if got := restartExecutable("/usr/local/bin/glorp (deleted)"); got != "/usr/local/bin/glorp" {
		t.Fatalf("restartExecutable = %q", got)
	}
	if got := restartExecutable(`C:\bin\glorp.exe`); got != `C:\bin\glorp.exe` {
		t.Fatalf("restartExecutable = %q", got)
	}
}
