//go:build production

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReleaseAssetsServeEmbeddedFrontend covers the embed path, which moved
// from the root package's web/dist to this package's own dist (issue #479): a
// wrong path fails the build, but a path that resolves to the wrong directory
// would only show up as a dashboard that serves nothing.
func TestReleaseAssetsServeEmbeddedFrontend(t *testing.T) {
	ui, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/some/client/route"} {
		response := httptest.NewRecorder()
		ui.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want %d", path, response.Code, http.StatusOK)
		}
		if body := response.Body.String(); !strings.Contains(body, "<div id=\"root\">") {
			t.Fatalf("GET %s did not serve the built dashboard: %q", path, body)
		}
	}
}
