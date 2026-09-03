package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lsegal/glorp/webui"
)

// TestWatchRefusesPublicWebUIWithoutTheWebUI keeps --web-ui-public from being
// accepted on a run that has no dashboard to publish, where it would otherwise
// start a tunnel to nothing.
func TestWatchRefusesPublicWebUIWithoutTheWebUI(t *testing.T) {
	for _, args := range [][]string{
		{"--ui", "none", "--web-ui-public", "owner/repo"},
		{"--ui", "tui", "--web-ui-public", "owner/repo"},
	} {
		if code := runWatch(args); code != 2 {
			t.Fatalf("runWatch(%v) = %d, want 2", args, code)
		}
	}
}

func TestWatchFlagSetHasPublicWebUIOffByDefault(t *testing.T) {
	flags := commandFlags("watch")
	flag := flags.Lookup("web-ui-public")
	if flag == nil {
		t.Fatal("watch has no --web-ui-public flag")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--web-ui-public default = %q, want false", flag.DefValue)
	}
	for _, required := range []string{"read-only", "token"} {
		if !strings.Contains(flag.Usage, required) {
			t.Errorf("--web-ui-public help does not say %q: %s", required, flag.Usage)
		}
	}
}

// TestAnnouncePublicWebUIKeepsTheTokenOffTheDashboard is the "printed exactly
// once, to the operator" half of issue #508: the terminal gets the link that
// opens the dashboard, and the dashboard's own log -- which every holder of
// that link can read over the tunnel -- gets the address without it.
func TestAnnouncePublicWebUIKeepsTheTokenOffTheDashboard(t *testing.T) {
	server, err := webui.New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	public, err := webui.NewPublicAccess(server)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := announcePublicWebUI(&out, server, public, "https://glorp.ngrok.app"); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, public.Token()) {
		t.Fatalf("terminal output = %q, want the access token", printed)
	}
	if strings.Count(printed, public.Token()) != 1 {
		t.Fatalf("terminal output prints the token %d times, want once", strings.Count(printed, public.Token()))
	}

	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state webui.State
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	logs := strings.Join(state.Logs, "\n")
	if strings.Contains(logs, public.Token()) {
		t.Fatalf("dashboard log = %q, want the token kept out of it", logs)
	}
	if !strings.Contains(logs, "https://glorp.ngrok.app") {
		t.Fatalf("dashboard log = %q, want the published address recorded", logs)
	}
}

func TestAnnouncePublicWebUISaysNothingWhenNotPublished(t *testing.T) {
	var out bytes.Buffer
	if err := announcePublicWebUI(&out, nil, nil, "https://glorp.ngrok.app"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing when the dashboard is not published", out.String())
	}
}
