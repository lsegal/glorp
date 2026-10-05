package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeTunnel struct {
	url    string
	closed bool
}

func (f *fakeTunnel) URL() string  { return f.url }
func (f *fakeTunnel) Close() error { f.closed = true; return nil }

// endpointConfigurer records the endpoint each webhook was configured with.
type endpointConfigurer struct{ endpoints []string }

func (c *endpointConfigurer) ConfigureWebhook(_ context.Context, _, endpoint, _ string) ([]string, error) {
	c.endpoints = append(c.endpoints, endpoint)
	return nil, nil
}

func newTestSupervisor(tunnel *fakeTunnel, gh webhookConfigurer, logs *[]string) *tunnelSupervisor {
	return &tunnelSupervisor{
		tunnel:      tunnel,
		webhookPath: "/webhook",
		reconciler:  newWebhookReconciler(gh, []string{"o/r"}, tunnel.url+"/webhook", "", func(string, ...interface{}) {}),
		logf:        func(format string, v ...interface{}) { *logs = append(*logs, fmt.Sprintf(format, v...)) },
	}
}

// TestTunnelSupervisorKeepsAHealthyTunnel checks a tunnel that still reaches
// the webhook server is left running and reconciled as before.
func TestTunnelSupervisorKeepsAHealthyTunnel(t *testing.T) {
	tunnel := &fakeTunnel{url: "https://one.ngrok.app"}
	gh := &endpointConfigurer{}
	var logs []string
	s := newTestSupervisor(tunnel, gh, &logs)
	s.probe = func(context.Context, string) error { return nil }
	s.start = func(context.Context) (webhookTunnel, error) {
		t.Fatal("a healthy tunnel was restarted")
		return nil, nil
	}
	s.reconcile(context.Background())
	if tunnel.closed || len(logs) != 0 {
		t.Fatalf("closed=%v logs=%q, want the tunnel left alone", tunnel.closed, logs)
	}
	if len(gh.endpoints) != 1 || gh.endpoints[0] != "https://one.ngrok.app/webhook" {
		t.Fatalf("configured %q, want the running tunnel's endpoint", gh.endpoints)
	}
}

// TestTunnelSupervisorRestartsADeadTunnel checks a tunnel that no longer
// reaches the webhook server -- ngrok died, or its session did not survive the
// machine sleeping -- is replaced, and the webhooks are pointed at the new URL
// in the same pass rather than left posting to a dead one (issue #683).
func TestTunnelSupervisorRestartsADeadTunnel(t *testing.T) {
	dead := &fakeTunnel{url: "https://one.ngrok.app"}
	gh := &endpointConfigurer{}
	var logs []string
	s := newTestSupervisor(dead, gh, &logs)
	s.probe = func(_ context.Context, endpoint string) error {
		if endpoint == "https://one.ngrok.app/webhook" {
			return errors.New("ngrok answered with ERR_NGROK_3200")
		}
		return nil
	}
	replacement := &fakeTunnel{url: "https://two.ngrok.app"}
	s.start = func(context.Context) (webhookTunnel, error) { return replacement, nil }
	s.reconcile(context.Background())
	if !dead.closed {
		t.Fatal("the dead tunnel was not stopped")
	}
	if s.tunnel != replacement {
		t.Fatal("the supervisor did not take the new tunnel")
	}
	if len(gh.endpoints) != 1 || gh.endpoints[0] != "https://two.ngrok.app/webhook" {
		t.Fatalf("configured %q, want the new tunnel's endpoint", gh.endpoints)
	}
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "not reachable") || !strings.Contains(joined, "https://two.ngrok.app") {
		t.Fatalf("logs %q, want the restart and the new URL reported", logs)
	}
	if err := s.Close(); err != nil || !replacement.closed {
		t.Fatalf("Close: %v, closed=%v, want the running tunnel stopped", err, replacement.closed)
	}
}

// TestTunnelSupervisorRetriesAFailedRestart checks a tunnel that cannot be
// started again -- the network is still coming back after a wake -- is tried
// again on the next check instead of being given up on.
func TestTunnelSupervisorRetriesAFailedRestart(t *testing.T) {
	dead := &fakeTunnel{url: "https://one.ngrok.app"}
	gh := &endpointConfigurer{}
	var logs []string
	s := newTestSupervisor(dead, gh, &logs)
	s.probe = func(_ context.Context, endpoint string) error {
		if strings.Contains(endpoint, "one") {
			return errors.New("no such host")
		}
		return nil
	}
	starts := 0
	s.start = func(context.Context) (webhookTunnel, error) {
		starts++
		if starts == 1 {
			return nil, errors.New("wait for ngrok tunnel: timed out")
		}
		return &fakeTunnel{url: "https://two.ngrok.app"}, nil
	}
	s.reconcile(context.Background())
	if s.tunnel != nil {
		t.Fatal("a tunnel is recorded after its restart failed")
	}
	s.reconcile(context.Background())
	if starts != 2 || s.tunnel == nil || s.tunnel.URL() != "https://two.ngrok.app" {
		t.Fatalf("starts=%d tunnel=%v, want the restart retried on the next check", starts, s.tunnel)
	}
	if last := gh.endpoints[len(gh.endpoints)-1]; last != "https://two.ngrok.app/webhook" {
		t.Fatalf("last configured endpoint %q, want the restarted tunnel's", last)
	}
}

// TestProbeTunnel checks the probe tells an endpoint that reaches the webhook
// server from one ngrok answers for itself because its agent is gone.
func TestProbeTunnel(t *testing.T) {
	server := httptest.NewServer(WebhookHandler{WebhookPath: "/webhook"})
	defer server.Close()
	if err := probeTunnel(context.Background(), server.URL+"/webhook"); err != nil {
		t.Fatalf("probe of a live webhook server: %v", err)
	}
	offline := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Ngrok-Error-Code", "ERR_NGROK_3200")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer offline.Close()
	if err := probeTunnel(context.Background(), offline.URL+"/webhook"); err == nil || !strings.Contains(err.Error(), "ERR_NGROK_3200") {
		t.Fatalf("probe of an offline ngrok endpoint: %v, want the ngrok error", err)
	}
	offline.Close()
	if err := probeTunnel(context.Background(), offline.URL+"/webhook"); err == nil {
		t.Fatal("probe of an unreachable endpoint succeeded")
	}
}
