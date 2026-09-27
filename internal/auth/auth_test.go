package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"page/internal/auth/oidctest"
	"page/internal/config"
)

// Recorder-based helpers: Allow writes its own rejection, so tests observe
// status codes and headers off a ResponseRecorder.

func getCheck(t *testing.T, c *Checker, path string, headers map[string]string, kind Kind) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c.Allow(w, r, kind)
	return w
}

func postCheck(t *testing.T, c *Checker, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c.Allow(w, r, KindAPI)
	return w
}

func TestCheckerTokenMode(t *testing.T) {
	c := NewChecker(config.AuthModeToken, "secret-1", nil)

	if w := getCheck(t, c, "/api/pages", map[string]string{"Authorization": "Bearer secret-1"}, KindAPI); w.Code != http.StatusOK {
		t.Fatalf("valid bearer = %d, want pass", w.Code)
	}
	w := getCheck(t, c, "/api/pages", nil, KindAPI)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer = %d, want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != `Bearer realm="api"` {
		t.Fatalf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
	}
	if w := getCheck(t, c, "/api/pages", map[string]string{"Authorization": "Bearer wrong"}, KindAPI); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d, want 401", w.Code)
	}
	// The UI shell stays public in token mode: browsers cannot send the
	// header on navigations; the API-driven prompt is the auth UX.
	if w := getCheck(t, c, "/", nil, KindBrowser); w.Code != http.StatusOK {
		t.Fatalf("token-mode shell = %d, want pass", w.Code)
	}
	// Prefix similarities must not authenticate.
	if w := getCheck(t, c, "/api/pages", map[string]string{"Authorization": "Bearer secret-1-extra"}, KindAPI); w.Code != http.StatusUnauthorized {
		t.Fatalf("prefix bearer = %d, want 401", w.Code)
	}
}

func TestCheckerNoneMode(t *testing.T) {
	c := NewChecker(config.AuthModeNone, "", nil)
	if w := getCheck(t, c, "/api/pages", nil, KindAPI); w.Code != http.StatusOK {
		t.Fatalf("none-mode API without credentials = %d, want pass", w.Code)
	}
	if w := getCheck(t, c, "/", nil, KindBrowser); w.Code != http.StatusOK {
		t.Fatalf("none-mode shell = %d, want pass", w.Code)
	}
}

func TestCheckerOIDCMode(t *testing.T) {
	idp := oidctest.New(t)
	o, err := NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer, ClientID: idp.ClientID,
		ClientSecret: idp.ClientSecret, RedirectURL: "https://app.test/auth/callback"}, "sess-secret")
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	c := NewChecker(config.AuthModeOIDC, "machine-token", o)

	// Machine path: bearer authenticates without a session, GET or POST.
	if w := getCheck(t, c, "/api/pages", map[string]string{"Authorization": "Bearer machine-token"}, KindAPI); w.Code != http.StatusOK {
		t.Fatalf("machine token GET = %d, want pass", w.Code)
	}
	if w := postCheck(t, c, "/api/pages", map[string]string{"Authorization": "Bearer machine-token"}); w.Code != http.StatusOK {
		t.Fatalf("machine token POST = %d, want pass (bearer is CSRF-exempt)", w.Code)
	}

	// Unauthenticated: API 401, browser redirects to /login.
	if w := getCheck(t, c, "/api/pages", nil, KindAPI); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API = %d, want 401", w.Code)
	}
	w := getCheck(t, c, "/", nil, KindBrowser)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated browser = %d %q, want 302 /login", w.Code, w.Header().Get("Location"))
	}

	// Session path, including the CSRF rule on non-GET.
	sess := httptest.NewRecorder() // any request works: Secure keys off TLS/proto
	c.oidc.mintSession(sess, httptest.NewRequest(http.MethodGet, "/", nil), "user-1", "u@x.test")
	session := sess.Header().Get("Set-Cookie")
	cookie := session[:firstSegment(session)]
	if w := getCheck(t, c, "/api/pages", map[string]string{"Cookie": cookie}, KindAPI); w.Code != http.StatusOK {
		t.Fatalf("session GET = %d, want pass", w.Code)
	}
	if w := postCheck(t, c, "/api/pages", map[string]string{"Cookie": cookie}); w.Code != http.StatusForbidden {
		t.Fatalf("session POST without CSRF header = %d, want 403", w.Code)
	}
	if w := postCheck(t, c, "/api/pages", map[string]string{
		"Cookie": cookie, CSRFHeader: CSRFValue,
	}); w.Code != http.StatusOK {
		t.Fatalf("session POST with CSRF header = %d, want pass", w.Code)
	}
	if w := postCheck(t, c, "/api/pages", map[string]string{
		"Cookie": cookie, CSRFHeader: "wrong",
	}); w.Code != http.StatusForbidden {
		t.Fatalf("session POST with wrong CSRF header = %d, want 403", w.Code)
	}
}

func firstSegment(setCookie string) int {
	for i := 0; i < len(setCookie); i++ {
		if setCookie[i] == ';' {
			return i
		}
	}
	return len(setCookie)
}

// TestCheckerOIDCWithoutOIDC is the mis-wired boot: the checker reports oidc
// mode but has no flow object, so nothing authenticates (fail closed).
func TestCheckerOIDCWithoutOIDC(t *testing.T) {
	c := NewChecker(config.AuthModeOIDC, "", nil)
	if w := getCheck(t, c, "/api/pages", nil, KindAPI); w.Code != http.StatusUnauthorized {
		t.Fatalf("API = %d, want 401", w.Code)
	}
	if w := getCheck(t, c, "/", nil, KindBrowser); w.Code != http.StatusFound {
		t.Fatalf("browser = %d, want redirect", w.Code)
	}
}

// TestNilCheckerFailsClosed proves a handler built without a checker rejects
// API traffic instead of panicking or opening up (review finding: a nil
// serve.Options.Auth must never be fail-open).
func TestNilCheckerFailsClosed(t *testing.T) {
	var c *Checker
	if w := getCheck(t, c, "/api/pages", nil, KindAPI); w.Code != http.StatusUnauthorized {
		t.Fatalf("nil-checker API = %d, want 401", w.Code)
	}
	if w := getCheck(t, c, "/api/pages", map[string]string{"Authorization": "Bearer x"}, KindAPI); w.Code != http.StatusUnauthorized {
		t.Fatalf("nil-checker API with a token = %d, want 401", w.Code)
	}
	if w := getCheck(t, c, "/", nil, KindBrowser); w.Code != http.StatusOK {
		t.Fatalf("nil-checker browser = %d, want public shell", w.Code)
	}
}
