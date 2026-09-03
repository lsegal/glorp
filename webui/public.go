package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// AccessPath reports which of the two dashboards a browser reached: the
// loopback one, which allows everything, or the published one, which does not.
// The frontend reads it so the retry, stop, and settings controls are absent
// from a remote view rather than present and failing.
const AccessPath = "/api/access"

// TokenParam carries the access token in the link glorp prints for the
// operator, since a phone browser cannot be given an Authorization header.
const TokenParam = "glorp_token"

// TokenCookie holds the token once such a link has been opened, so the token
// leaves the address bar and later requests carry it on their own.
const TokenCookie = "glorp_web_ui_token"

// Access is what the /api/access endpoint reports.
type Access struct {
	ReadOnly bool `json:"readOnly"`
}

// PublicAccess is the dashboard as published beyond loopback (issue #508): the
// same handler behind a generated token, and read-only. The dashboard shows
// repository names, issue titles, and every line an agent prints, so a URL a
// tunnel makes reachable from anywhere is not allowed to be an open one, and
// the token is not allowed to double as authority to retry, stop, or
// reconfigure the run it is looking at.
type PublicAccess struct {
	dashboard http.Handler
	token     string
}

// NewPublicAccess wraps dashboard with a freshly generated token.
func NewPublicAccess(dashboard http.Handler) (*PublicAccess, error) {
	token, err := newAccessToken()
	if err != nil {
		return nil, err
	}
	return &PublicAccess{dashboard: dashboard, token: token}, nil
}

func newAccessToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate web UI access token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Token is the credential the published dashboard requires.
func (p *PublicAccess) Token() string { return p.token }

// LinkURL is the address to hand the operator: the tunnel's public URL with
// the token in it. Opening it stores the token as a cookie and redirects to
// the same page without it.
func (p *PublicAccess) LinkURL(publicURL string) (string, error) {
	u, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid public web UI URL %q", publicURL)
	}
	u.Path = "/"
	query := u.Query()
	query.Set(TokenParam, p.token)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (p *PublicAccess) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if token := r.URL.Query().Get(TokenParam); token != "" {
		if !p.matches(token) {
			p.deny(w)
			return
		}
		p.grant(w, r)
		return
	}
	if !p.authorized(r) {
		p.deny(w)
		return
	}
	if r.URL.Path == AccessPath {
		writeAccess(w, true)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "the published dashboard is read-only", http.StatusForbidden)
		return
	}
	p.dashboard.ServeHTTP(w, r)
}

// Mux routes webhookPath to webhook and everything else to the published
// dashboard, so the one ngrok tunnel a push-mode run already has carries both
// rather than a second tunnel competing for the account's agent session.
func (p *PublicAccess) Mux(webhookPath string, webhook http.Handler) http.Handler {
	path := normalizeWebhookPath(webhookPath)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path || strings.HasPrefix(r.URL.Path, path+"/") {
			webhook.ServeHTTP(w, r)
			return
		}
		p.ServeHTTP(w, r)
	})
}

func normalizeWebhookPath(path string) string {
	if path == "" {
		return "/webhook"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(path, "/")
}

// grant records the token as a cookie and sends the browser back to the same
// page without it, so the credential is not left in the address bar, in the
// history, or in a Referer header the page's own requests would carry.
func (p *PublicAccess) grant(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     TokenCookie,
		Value:    p.token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	redirect := *r.URL
	query := redirect.Query()
	query.Del(TokenParam)
	redirect.RawQuery = query.Encode()
	if redirect.Path == "" {
		redirect.Path = "/"
	}
	http.Redirect(w, r, redirect.RequestURI(), http.StatusFound)
}

func (p *PublicAccess) authorized(r *http.Request) bool {
	if cookie, err := r.Cookie(TokenCookie); err == nil && p.matches(cookie.Value) {
		return true
	}
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && p.matches(strings.TrimSpace(value))
}

func (p *PublicAccess) matches(value string) bool {
	return subtle.ConstantTimeCompare([]byte(value), []byte(p.token)) == 1
}

func (p *PublicAccess) deny(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="glorp dashboard"`)
	http.Error(w, "the glorp dashboard needs the access token printed when this run started", http.StatusUnauthorized)
}

func writeAccess(w http.ResponseWriter, readOnly bool) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(Access{ReadOnly: readOnly})
}
