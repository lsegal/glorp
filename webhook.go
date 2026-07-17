package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type WebhookEvent struct {
	Kind        string
	Action      string
	Repository  string
	Ref         string
	Before      string
	After       string
	CommitCount int
	IssueNumber int
	IssueTitle  string
}

// WebhookHandler accepts GitHub webhook deliveries, records useful delivery
// details, and asks the glorp to refresh its issue list. The payload is not
// used as the source of issue data: the next authenticated GitHub CLI query
// remains authoritative.
type WebhookHandler struct {
	Events      chan<- WebhookEvent
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
		event := decodeWebhookEvent(r.Header.Get("X-GitHub-Event"), body)
		select {
		case h.Events <- event:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func decodeWebhookEvent(kind string, body []byte) WebhookEvent {
	event := WebhookEvent{Kind: kind}
	var payload struct {
		Action     string `json:"action"`
		Ref        string `json:"ref"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Issue struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
		} `json:"issue"`
		Commits []json.RawMessage `json:"commits"`
	}
	if json.Unmarshal(body, &payload) == nil {
		event.Action = payload.Action
		event.Repository = payload.Repository.FullName
		event.Ref = payload.Ref
		event.Before = payload.Before
		event.After = payload.After
		event.CommitCount = len(payload.Commits)
		event.IssueNumber = payload.Issue.Number
		event.IssueTitle = payload.Issue.Title
	}
	return event
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
