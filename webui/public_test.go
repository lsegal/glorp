package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dashboardStub stands in for the real dashboard so a test can tell whether a
// request reached it at all, which is what the guard exists to control.
type dashboardStub struct{ reached bool }

func (d *dashboardStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.reached = true
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("dashboard"))
}

func newTestPublicAccess(t *testing.T) (*PublicAccess, *dashboardStub) {
	t.Helper()
	dashboard := &dashboardStub{}
	public, err := NewPublicAccess(dashboard)
	if err != nil {
		t.Fatal(err)
	}
	return public, dashboard
}

func TestPublicAccessDeniesRequestsWithoutTheToken(t *testing.T) {
	public, dashboard := newTestPublicAccess(t)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/", nil),
		httptest.NewRequest(http.MethodGet, "/api/state", nil),
		httptest.NewRequest(http.MethodGet, "/?"+TokenParam+"=wrong", nil),
	} {
		response := httptest.NewRecorder()
		public.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s: response = %d, want 401", request.URL, response.Code)
		}
	}
	// A cookie carrying somebody else's guess is no better than none.
	request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	request.AddCookie(&http.Cookie{Name: TokenCookie, Value: public.Token() + "x"})
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("response = %d, want 401 for a wrong cookie", response.Code)
	}
	if dashboard.reached {
		t.Fatal("an unauthorized request reached the dashboard")
	}
}

func TestPublicAccessLinkStoresTheTokenAndRedirectsWithoutIt(t *testing.T) {
	public, dashboard := newTestPublicAccess(t)
	link, err := public.LinkURL("https://glorp.ngrok.app/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, TokenParam+"=") || !strings.Contains(link, public.Token()) {
		t.Fatalf("link = %q, want it to carry the access token", link)
	}

	response := httptest.NewRecorder()
	public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/?"+TokenParam+"="+public.Token(), nil))
	if response.Code != http.StatusFound {
		t.Fatalf("response = %d, want 302", response.Code)
	}
	if location := response.Header().Get("Location"); strings.Contains(location, TokenParam) {
		t.Fatalf("redirect = %q, want the token stripped from the address", location)
	}
	if dashboard.reached {
		t.Fatal("the arming request served dashboard content instead of redirecting")
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != TokenCookie || cookies[0].Value != public.Token() {
		t.Fatalf("cookies = %#v, want the access token stored once", cookies)
	}
	if !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("cookie = %#v, want it HttpOnly and Secure", cookies[0])
	}

	// The stored cookie is what every later request is admitted on.
	next := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	next.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	public.ServeHTTP(response, next)
	if response.Code != http.StatusOK || !dashboard.reached {
		t.Fatalf("response = %d, reached = %v, want the dashboard served", response.Code, dashboard.reached)
	}
}

func TestPublicAccessAcceptsABearerToken(t *testing.T) {
	public, dashboard := newTestPublicAccess(t)
	request := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	request.Header.Set("Authorization", "Bearer "+public.Token())
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !dashboard.reached {
		t.Fatalf("response = %d, reached = %v, want the dashboard served", response.Code, dashboard.reached)
	}
}

// The token buys a view of the run, not authority over it: retry, stop, and the
// settings modal all POST, and none of them may cross the tunnel.
func TestPublicAccessIsReadOnly(t *testing.T) {
	public, dashboard := newTestPublicAccess(t)
	for _, path := range []string{"/api/jobs/action", "/api/settings"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+public.Token())
		response := httptest.NewRecorder()
		public.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s: response = %d, want 403", path, response.Code)
		}
		if dashboard.reached {
			t.Fatalf("%s: a write reached the dashboard", path)
		}
	}
}

func TestAccessEndpointDistinguishesThePublishedDashboard(t *testing.T) {
	public, _ := newTestPublicAccess(t)
	request := httptest.NewRequest(http.MethodGet, AccessPath, nil)
	request.Header.Set("Authorization", "Bearer "+public.Token())
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	var access Access
	if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !access.ReadOnly {
		t.Fatalf("published access = %d %#v, want read-only", response.Code, access)
	}

	server, err := New("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, AccessPath, nil))
	if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || access.ReadOnly {
		t.Fatalf("loopback access = %d %#v, want full access", response.Code, access)
	}
}

// Push-mode runs carry both the webhook and the dashboard over the one tunnel
// ngrok's single agent session allows, so the webhook path must stay exactly as
// unguarded as it is today while everything else needs the token.
func TestPublicAccessMuxKeepsTheWebhookPathUnguarded(t *testing.T) {
	public, dashboard := newTestPublicAccess(t)
	webhook := &dashboardStub{}
	mux := public.Mux("/hooks", webhook)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/hooks", strings.NewReader("{}")))
	if response.Code != http.StatusOK || !webhook.reached {
		t.Fatalf("response = %d, reached = %v, want the webhook handler", response.Code, webhook.reached)
	}

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized || dashboard.reached {
		t.Fatalf("response = %d, reached = %v, want the dashboard guarded", response.Code, dashboard.reached)
	}
}

func TestPublicAccessTokensAreUnpredictable(t *testing.T) {
	first, _ := newTestPublicAccess(t)
	second, _ := newTestPublicAccess(t)
	if first.Token() == second.Token() {
		t.Fatal("two runs generated the same access token")
	}
	if len(first.Token()) < 32 {
		t.Fatalf("token = %q, want at least 32 characters of entropy", first.Token())
	}
}

func TestLinkURLRejectsAnUnusablePublicURL(t *testing.T) {
	public, _ := newTestPublicAccess(t)
	if _, err := public.LinkURL("not a url"); err == nil {
		t.Fatal("LinkURL accepted a public URL with no scheme or host")
	}
}
