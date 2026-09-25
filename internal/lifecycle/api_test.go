package lifecycle

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The bearer check must reject before the service is ever touched — a nil
// service proves no DB/storage access happens on the unauthorized path.
func TestAPIRejectsUnauthenticated(t *testing.T) {
	api := NewAPI(nil, "secret")
	srv := httptest.NewServer(api.Park())
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api/pages/x-1/park", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", resp.StatusCode)
	}

	req, _ = http.NewRequest("POST", srv.URL+"/api/pages/x-1/park", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", resp.StatusCode)
	}

	// Traversal and empty slugs are treated as unknown pages.
	api2 := NewAPI(nil, "secret")
	srv2 := httptest.NewServer(api2.Unpark())
	defer srv2.Close()
	for _, slug := range []string{"..", "a%2Fb"} {
		req, _ := http.NewRequest("POST", srv2.URL+"/api/pages/"+slug+"/unpark", nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", slug, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("slug %q status = %d, want 404", slug, resp.StatusCode)
		}
	}
}

func TestValidSlug(t *testing.T) {
	for _, s := range []string{"a-1", "landing-page-12"} {
		if !validSlug(s) {
			t.Fatalf("validSlug(%q) = false", s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, ParkedPrefix} {
		if validSlug(s) {
			t.Fatalf("validSlug(%q) = true, want false", s)
		}
	}
}
