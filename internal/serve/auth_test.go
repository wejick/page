package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"page/internal/auth"
	"page/internal/auth/oidctest"
	"page/internal/config"
	"page/internal/lifecycle"
	"page/internal/storage/mem"
	"page/internal/upload"
)

// seededStore builds a mem store preloaded with the seed objects.
func seededStore(t *testing.T, seed map[string][2]string) *mem.Store {
	t.Helper()
	s := mem.New()
	for key, v := range seed {
		if err := s.Put(context.Background(), key, v[0], strings.NewReader(v[1])); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	return s
}

// authModeRouter builds the full admin/all router under one auth mode.
func authModeRouter(t *testing.T, seed map[string][2]string, mode config.AuthMode) (*httptest.Server, *auth.Checker) {
	t.Helper()
	var oidcFlow *auth.OIDC
	if mode == config.AuthModeOIDC {
		idp := oidctest.New(t)
		flow, err := auth.NewOIDC(t.Context(), config.OIDC{Issuer: idp.Issuer,
			ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			RedirectURL: "https://app.test/auth/callback"}, "test-session-secret", nil)
		if err != nil {
			t.Fatalf("NewOIDC: %v", err)
		}
		oidcFlow = flow
	}
	checker := auth.NewChecker(mode, testToken, oidcFlow)
	o := Options{Store: seededStore(t, seed), Upload: upload.New(upload.Options{Auth: checker}),
		Lifecycle: lifecycle.NewAPI(lifecycle.New(nil, nil, nil), checker, nil, nil), Auth: checker}
	ts := httptest.NewServer(New(o))
	t.Cleanup(ts.Close)
	return ts, checker
}

func noFollowClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func doJSON(t *testing.T, ts *httptest.Server, method, path string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := noFollowClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

// TestAuthModeRouting exercises the per-mode admin-plane surface at the
// router level: which auth routes are mounted, what the UI advertises, and
// the unauthenticated outcomes (auth-modes D5, D7, D8). Handler bodies that
// need a database are exercised in internal/auth and internal/e2e.
func TestAuthModeRouting(t *testing.T) {
	seed := map[string][2]string{
		"s-1/index.html": {"text/html; charset=utf-8", "<h1>hi</h1>"},
	}

	t.Run("token mode", func(t *testing.T) {
		ts, _ := authModeRouter(t, seed, config.AuthModeToken)
		status, _, body := doJSON(t, ts, "GET", "/", nil)
		if status != 200 || !strings.Contains(body, `data-auth-mode="token"`) {
			t.Fatalf("shell = %d %q, want 200 advertising token mode", status, body)
		}
		if status, _, _ = doJSON(t, ts, "GET", "/login", nil); status != 404 {
			t.Fatalf("/login = %d, want 404 (not mounted)", status)
		}
		if status, _, _ = doJSON(t, ts, "POST", "/logout", nil); status != 404 {
			t.Fatalf("/logout = %d, want 404 (not mounted)", status)
		}
		status, _, _ = doJSON(t, ts, "GET", "/api/pages", nil)
		if status != 401 {
			t.Fatalf("unauthenticated list = %d, want 401", status)
		}
	})

	t.Run("none mode", func(t *testing.T) {
		ts, _ := authModeRouter(t, seed, config.AuthModeNone)
		status, _, body := doJSON(t, ts, "GET", "/", nil)
		if status != 200 || !strings.Contains(body, `data-auth-mode="none"`) {
			t.Fatalf("shell = %d %q, want 200 advertising none mode", status, body)
		}
	})

	t.Run("nil Auth fails closed", func(t *testing.T) {
		// A router wired without a checker must reject API traffic, not
		// open it (auth-modes D1).
		o := Options{Store: seededStore(t, seed),
			Upload:    upload.New(upload.Options{DB: nil, Store: seededStore(t, seed)}),
			Lifecycle: lifecycle.NewAPI(lifecycle.New(nil, nil, nil), nil, nil, nil)}
		ts := httptest.NewServer(New(o))
		t.Cleanup(ts.Close)
		if status, _, _ := doJSON(t, ts, "GET", "/api/pages", nil); status != 401 {
			t.Fatalf("nil-Auth list = %d, want 401", status)
		}
		if status, _, _ := doJSON(t, ts, "POST", "/api/pages/s-1/park", nil); status != 401 {
			t.Fatalf("nil-Auth park = %d, want 401", status)
		}
	})

	t.Run("oidc mode", func(t *testing.T) {
		ts, checker := authModeRouter(t, seed, config.AuthModeOIDC)
		status, _, _ := doJSON(t, ts, "GET", "/", nil)
		if status != http.StatusFound {
			t.Fatalf("unauthenticated shell = %d, want 302", status)
		}
		status, _, _ = doJSON(t, ts, "GET", "/api/pages", nil)
		if status != 401 {
			t.Fatalf("unauthenticated list = %d, want 401", status)
		}
		status, header, _ := doJSON(t, ts, "GET", "/login", nil)
		if status != http.StatusFound || !strings.Contains(header.Get("Location"), "/authorize") {
			t.Fatalf("/login = %d %q, want a redirect to the IdP", status, header.Get("Location"))
		}
		if status, _, _ = doJSON(t, ts, "GET", "/auth/callback?code=x&state=y", nil); status != 401 {
			t.Fatalf("cookieless callback = %d, want 401", status)
		}
		if status, _, _ = doJSON(t, ts, "POST", "/logout", nil); status != 200 {
			t.Fatalf("logout = %d, want 200", status)
		}
		// The serve plane never mounts the flow, even in oidc mode.
		serveOnly := httptest.NewServer(New(Options{Store: seededStore(t, seed), Auth: checker,
			Mode: config.ModeServe}))
		defer serveOnly.Close()
		if status, _, _ = doJSON(t, serveOnly, "GET", "/login", nil); status != 404 {
			t.Fatalf("serve-mode /login = %d, want 404", status)
		}
	})
}
