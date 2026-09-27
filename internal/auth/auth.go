// Package auth authenticates admin-plane requests. One Checker serves every
// admin handler; the mode comes from AUTH_MODE (auth-modes D1): token (the
// static bearer, historical default), none (the network/SSO proxy is the
// trust boundary), or oidc (this app is the OIDC relying party and mints a
// stateless signed session cookie, with the bearer kept as the machine path
// — auth-modes D2, D5). The serve plane never touches this package.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"page/internal/config"
)

// Kind tells the Checker which surface a request targets: API routes answer
// 401, browser routes redirect to /login in oidc mode (auth-modes D7). In
// token mode the UI shell stays public — a browser navigation cannot carry
// an Authorization header, so the API-driven 401 prompt is the auth UX.
type Kind int

const (
	KindAPI Kind = iota
	KindBrowser
)

// CSRF protection for the cookie session (auth-modes D6): a custom header
// forces a CORS preflight, and the app sends no CORS headers, so cross-site
// JavaScript cannot set it. Bearer-authenticated requests are exempt — they
// carry no ambient credential.
const (
	CSRFHeader = "X-Requested-With"
	CSRFValue  = "page-ui"
)

// Checker is the shared admin-plane authenticator (auth-modes D7).
type Checker struct {
	mode  config.AuthMode
	token string
	oidc  *OIDC
}

// NewChecker builds the admin-plane authenticator. oidc must be non-nil for
// AuthModeOIDC boots (cmd/server constructs it with NewOIDC); without it
// only the bearer machine path can authenticate.
func NewChecker(mode config.AuthMode, token string, oidc *OIDC) *Checker {
	return &Checker{mode: mode, token: token, oidc: oidc}
}

// Mode reports the configured auth mode.
func (c *Checker) Mode() config.AuthMode { return c.mode }

// HasFlow reports whether the oidc browser flow is wired. The router mounts
// /login, /auth/callback and /logout only when this is true; without a flow
// the checker still authenticates (machine path only) and fails closed.
func (c *Checker) HasFlow() bool { return c != nil && c.oidc != nil }

// Login, Callback and Logout expose the oidc-mode browser flow. They return
// nil unless HasFlow; the router mounts them only then.
func (c *Checker) Login() http.Handler {
	if c.oidc == nil {
		return nil
	}
	return c.oidc.Login()
}

func (c *Checker) Callback() http.Handler {
	if c.oidc == nil {
		return nil
	}
	return c.oidc.Callback()
}

func (c *Checker) Logout() http.Handler {
	if c.oidc == nil {
		return nil
	}
	return c.oidc.Logout()
}

// Allow reports whether the request may proceed, writing the rejection
// itself (401, 403, or the oidc /login redirect). A nil Checker — a handler
// built without one — fails closed: API traffic is rejected with 401, the
// browser shell stays public as in token mode (auth-modes D1).
func (c *Checker) Allow(w http.ResponseWriter, r *http.Request, kind Kind) bool {
	if c == nil {
		if kind == KindBrowser {
			return true
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	switch c.mode {
	case config.AuthModeNone:
		return true

	case config.AuthModeOIDC:
		if c.bearerOK(r) { // machine path (auth-modes D5)
			return true
		}
		if c.oidc != nil {
			if _, ok := c.oidc.sessionFrom(r); ok {
				if r.Method != http.MethodGet && r.Header.Get(CSRFHeader) != CSRFValue {
					http.Error(w, "missing "+CSRFHeader+" header", http.StatusForbidden)
					return false
				}
				return true
			}
		}
		if kind == KindBrowser {
			http.Redirect(w, r, "/login", http.StatusFound)
			return false
		}
		c.unauthorized(w)
		return false

	default: // config.AuthModeToken
		if kind == KindBrowser || c.bearerOK(r) {
			return true
		}
		c.unauthorized(w)
		return false
	}
}

// unauthorized answers with the same shape the token mode always sent.
func (c *Checker) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// bearerOK is the constant-time compare the token mode has always used
// (static-page-hosting D9 semantics, retained).
func (c *Checker) bearerOK(r *http.Request) bool {
	if c.token == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.token)) == 1
}
