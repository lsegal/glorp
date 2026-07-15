package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookHandlerTriggersSupportedEvents(t *testing.T) {
	events := make(chan struct{}, 1)
	h := WebhookHandler{Events: events, WebhookPath: "/webhook"}
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"action":"opened"}`))
	req.Header.Set("X-GitHub-Event", "issues")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusAccepted)
	}
	select {
	case <-events:
	default:
		t.Fatal("webhook did not trigger a refresh")
	}
}

func TestWebhookHandlerValidatesSignature(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", fmt.Sprintf("sha256=%x", mac.Sum(nil)))
	res := httptest.NewRecorder()
	WebhookHandler{Events: make(chan struct{}, 1), Secret: secret, WebhookPath: "/webhook"}.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusAccepted)
	}

	bad := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	bad.Header.Set("X-GitHub-Event", "push")
	bad.Header.Set("X-Hub-Signature-256", "sha256=00")
	badRes := httptest.NewRecorder()
	WebhookHandler{Events: make(chan struct{}, 1), Secret: secret, WebhookPath: "/webhook"}.ServeHTTP(badRes, bad)
	if badRes.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want %d", badRes.Code, http.StatusUnauthorized)
	}
}
