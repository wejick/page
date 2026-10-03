package serve

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getBody(t *testing.T, ts *httptest.Server, path string, cookie *http.Cookie) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest("GET", ts.URL+path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

// TestUIServesAssets covers the /ui/* asset route: exact content types,
// cache policy split between the immutable vendored Alpine file and the
// application assets that revalidate, and 404 for everything else —
// including traversal-shaped names (harden-admin-ui D2).
func TestUIServesAssets(t *testing.T) {
	ts, _ := newTestHandler(t, nil)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCT     string
		wantCache  string
	}{
		{name: "app css", path: "/ui/app.css", wantStatus: 200,
			wantCT: "text/css; charset=utf-8", wantCache: cacheControlUIAsset},
		{name: "app js", path: "/ui/app.js", wantStatus: 200,
			wantCT: "text/javascript; charset=utf-8", wantCache: cacheControlUIAsset},
		{name: "vendored alpine immutable", path: "/ui/alpine.csp-3.17.4.min.js",
			wantStatus: 200, wantCT: "text/javascript; charset=utf-8",
			wantCache: cacheControlAsset},
		{name: "unknown file", path: "/ui/nope.js", wantStatus: 404},
		{name: "html not served at /ui", path: "/ui/index.html", wantStatus: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, header, _ := getBody(t, ts, tt.path, nil)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if tt.wantCT != "" && header.Get("Content-Type") != tt.wantCT {
				t.Fatalf("Content-Type = %q, want %q", header.Get("Content-Type"), tt.wantCT)
			}
			if tt.wantCache != "" && header.Get("Cache-Control") != tt.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", header.Get("Cache-Control"), tt.wantCache)
			}
			if tt.wantStatus == 200 && header.Get("ETag") == "" {
				t.Fatal("200 asset response missing ETag")
			}
		})
	}

	// no-cache means revalidate: a matching If-None-Match yields 304.
	_, header, _ := getBody(t, ts, "/ui/app.js", nil)
	etag := header.Get("ETag")
	req, _ := http.NewRequest("GET", ts.URL+"/ui/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidate = %d, want 304", resp.StatusCode)
	}
}

// TestUIThemeInjection proves the sp-theme cookie drives the data-theme
// attribute on <html> before first paint, that only valid choices inject
// (anything else means "system": no attribute, prefers-color-scheme wins),
// and that the auth-mode injection is untouched (harden-admin-ui D4).
func TestUIThemeInjection(t *testing.T) {
	ts, _ := newTestHandler(t, nil)

	tests := []struct {
		name       string
		cookie     string
		wantSubstr string
		notWant    string
	}{
		{name: "dark choice", cookie: "dark", wantSubstr: `<html lang="en" data-theme="dark">`},
		{name: "light choice", cookie: "light", wantSubstr: `<html lang="en" data-theme="light">`},
		{name: "system means no injection", cookie: "system", notWant: `data-theme="dark"`},
		{name: "junk means no injection", cookie: "purple", notWant: `data-theme="`},
		{name: "no cookie", notWant: `data-theme="`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cookie *http.Cookie
			if tt.cookie != "" {
				cookie = &http.Cookie{Name: "sp-theme", Value: tt.cookie}
			}
			status, _, body := getBody(t, ts, "/", cookie)
			if status != 200 {
				t.Fatalf("status = %d, want 200", status)
			}
			if tt.wantSubstr != "" && !strings.Contains(body, tt.wantSubstr) {
				t.Fatalf("body missing %q", tt.wantSubstr)
			}
			if tt.notWant != "" && strings.Contains(body, tt.notWant) {
				t.Fatalf("body unexpectedly contains %q", tt.notWant)
			}
			if !strings.Contains(body, `data-auth-mode="token"`) {
				t.Fatal("auth-mode injection lost")
			}
		})
	}
}

// TestUISecurityHeaders asserts the strict CSP and nosniff on the shell and
// its assets, and that the API — which returns JSON, not documents — is not
// stamped (harden-admin-ui D3).
func TestUISecurityHeaders(t *testing.T) {
	ts, _ := newTestHandler(t, nil)

	for _, path := range []string{"/", "/ui/app.js", "/ui/app.css"} {
		_, header, _ := getBody(t, ts, path, nil)
		csp := header.Get("Content-Security-Policy")
		if csp != uiContentSecurityPolicy {
			t.Fatalf("%s CSP = %q, want %q", path, csp, uiContentSecurityPolicy)
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Fatalf("%s CSP must not allow unsafe-inline/unsafe-eval: %q", path, csp)
		}
		if header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing X-Content-Type-Options: nosniff", path)
		}
	}

	// The bare test router mounts no API handlers (nil Upload), so this
	// 404s; the assertion is that the shell CSP never leaks onto API
	// responses.
	_, apiHeader, _ := getBody(t, ts, "/api/pages", nil)
	if apiHeader.Get("Content-Security-Policy") != "" {
		t.Fatal("API response should not carry the shell CSP")
	}
}
