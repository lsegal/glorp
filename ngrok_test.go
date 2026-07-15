package main

import "testing"

func TestDecodeNgrokURLPrefersHTTPS(t *testing.T) {
	got, err := decodeNgrokURL([]byte(`{"tunnels":[{"public_url":"http://old.ngrok.app","proto":"http"},{"public_url":"https://new.ngrok.app/","proto":"https"}]}`))
	if err != nil || got != "https://new.ngrok.app" {
		t.Fatalf("URL = %q, error = %v", got, err)
	}
}

func TestWebhookURLUsesNgrokHost(t *testing.T) {
	got, err := webhookURL("https://example.ngrok.app/", "hooks")
	if err != nil || got != "https://example.ngrok.app/hooks" {
		t.Fatalf("URL = %q, error = %v", got, err)
	}
}

func TestNgrokURLIdentifiesStaleTunnel(t *testing.T) {
	if !ngrokURL("https://old.ngrok-free.app/webhook") {
		t.Fatal("ngrok URL was not identified")
	}
	if ngrokURL("https://hooks.example.com/webhook") {
		t.Fatal("unrelated webhook URL was identified as ngrok")
	}
}
