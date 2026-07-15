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
	events := make(chan WebhookEvent, 1)
	h := WebhookHandler{Events: events, WebhookPath: "/webhook"}
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"action":"opened"}`))
	req.Header.Set("X-GitHub-Event", "issues")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusAccepted)
	}
	select {
	case event := <-events:
		if event.Kind != "issues" || event.Action != "opened" {
			t.Fatalf("event = %#v", event)
		}
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
	WebhookHandler{Events: make(chan WebhookEvent, 1), Secret: secret, WebhookPath: "/webhook"}.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusAccepted)
	}

	bad := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	bad.Header.Set("X-GitHub-Event", "push")
	bad.Header.Set("X-Hub-Signature-256", "sha256=00")
	badRes := httptest.NewRecorder()
	WebhookHandler{Events: make(chan WebhookEvent, 1), Secret: secret, WebhookPath: "/webhook"}.ServeHTTP(badRes, bad)
	if badRes.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want %d", badRes.Code, http.StatusUnauthorized)
	}
}

func TestDecodeWebhookEventIncludesPushDetails(t *testing.T) {
	event := decodeWebhookEvent("push", []byte(`{"ref":"refs/heads/main","before":"abc","after":"def","repository":{"full_name":"o/r"},"commits":[{},{}]}`))
	if event.Kind != "push" || event.Repository != "o/r" || event.Ref != "refs/heads/main" || event.Before != "abc" || event.After != "def" || event.CommitCount != 2 {
		t.Fatalf("event = %#v", event)
	}
}

func TestDecodeWebhookEventIncludesIssueDetails(t *testing.T) {
	event := decodeWebhookEvent("issues", []byte(`{"action":"opened","repository":{"full_name":"o/r"},"issue":{"number":54,"title":"new bug"}}`))
	if event.Kind != "issues" || event.Action != "opened" || event.Repository != "o/r" || event.IssueNumber != 54 || event.IssueTitle != "new bug" {
		t.Fatalf("event = %#v", event)
	}
}
