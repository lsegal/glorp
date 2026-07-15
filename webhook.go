package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// WebhookHandler accepts GitHub webhook deliveries and asks the watcher to
// refresh its issue list. The issue payload is deliberately not interpreted:
// the next authenticated GitHub CLI query remains the source of truth.
type WebhookHandler struct {
	Events      chan<- struct{}
	Secret      string
	WebhookPath string
}

func (h WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.WebhookPath != "" && r.URL.Path != h.WebhookPath {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if h.Secret != "" && !validWebhookSignature(h.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	switch r.Header.Get("X-GitHub-Event") {
	case "issues", "push", "ping":
		select {
		case h.Events <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func validWebhookSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	expected := hmac.New(sha256.New, []byte(secret))
	_, _ = expected.Write(body)
	actual, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	return err == nil && hmac.Equal(actual, expected.Sum(nil))
}
