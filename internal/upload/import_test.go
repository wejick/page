//go:build integration

package upload

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"page/internal/fetch"
)

// importSource serves a minimal page with one stylesheet (200) and one
// image whose status the test picks (200 for the happy path, 403 for the
// strict gate).
func importSource(t *testing.T, assetStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/post/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/post/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<!doctype html><html><head>` +
			`<link rel="stylesheet" href="assets/style.css"></head>` +
			`<body><img src="assets/hero.png"></body></html>`))
	})
	mux.HandleFunc("/post/assets/style.css", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body{color:red}"))
	})
	mux.HandleFunc("/post/assets/hero.png", func(w http.ResponseWriter, r *http.Request) {
		if assetStatus != http.StatusOK {
			http.Error(w, "nope", assetStatus)
			return
		}
		_, _ = w.Write([]byte("PNG1"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// urlBody builds the multipart form for a URL import (no file).
func urlBody(t *testing.T, srcURL, identifier string) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	if identifier != "" {
		_ = mw.WriteField("identifier", identifier)
	}
	_ = mw.WriteField("url", srcURL)
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf, mw.FormDataContentType()
}

func TestImportByURL(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newTestHandler(t, ctx, 0, fetch.Permissive)
	srv := importSource(t, http.StatusOK)

	body, ctype := urlBody(t, srv.URL+"/post/", "imported")
	rec := post(h, body, ctype, "secret")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", rec.Code, rec.Body)
	}
	for _, want := range []string{`"slug":"imported-1"`, `"baked":2`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("missing %s in %s", want, rec.Body)
		}
	}
	// Entry stored; the relative assets baked from the source URL.
	for _, k := range []string{"imported-1/index.html", "imported-1/external/127.0.0.1/post/assets/hero.png"} {
		if _, err := store.Stat(ctx, k); err != nil {
			t.Fatalf("object %s missing: %v", k, err)
		}
	}
	if rec2 := getMeta(h, "imported-1"); rec2.Code != 200 ||
		!bytes.Contains(rec2.Body.Bytes(), []byte(`"status":"baked"`)) {
		t.Fatalf("metadata status=%d body=%s", rec2.Code, rec2.Body)
	}
}

func TestImportSourceValidation(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newTestHandler(t, ctx, 0, fetch.Permissive)

	// Both sources → 422.
	both, ctype := multipartBody(t, "p.html", []byte("<h1>x</h1>"), map[string]string{"url": "https://example.com/"})
	if rec := post(h, both, ctype, "secret"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("both sources status = %d body=%s, want 422", rec.Code, rec.Body)
	}
	// Neither → 400.
	neither, ctype2 := urlBody(t, "", "")
	if rec := post(h, neither, ctype2, "secret"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no source status = %d body=%s, want 400", rec.Code, rec.Body)
	}
	if store.Len() != 0 {
		t.Fatalf("%d objects stored after validation failures, want 0", store.Len())
	}
}

func TestImportEntryFetchFailures(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newTestHandler(t, ctx, 0, fetch.Permissive)
	capped, _, _ := newTestHandler(t, ctx, 16, fetch.Permissive) // entry cap 16 bytes
	blocked, _, _ := newTestHandler(t, ctx, 0, fetch.Standard)   // refuses loopback sources

	jsonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"not":"html"}`))
	}))
	defer jsonSrv.Close()
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>" + strings.Repeat("x", 64) + "</body></html>"))
	}))
	defer bigSrv.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	cases := []struct {
		name string
		url  string
		h    *Handler
		want int
	}{
		{"unreachable", deadURL, h, http.StatusBadGateway},
		{"not html", jsonSrv.URL, h, http.StatusUnsupportedMediaType},
		{"invalid scheme", "file:///etc/passwd", h, http.StatusUnprocessableEntity},
		{"oversize entry", bigSrv.URL, capped, http.StatusRequestEntityTooLarge},
		{"guard blocked", jsonSrv.URL, blocked, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ctype := urlBody(t, tc.url, "")
			rec := post(tc.h, body, ctype, "secret")
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body, tc.want)
			}
		})
	}
	if store.Len() != 0 {
		t.Fatalf("%d objects stored after fetch failures, want 0", store.Len())
	}
}

func TestImportStrictGate(t *testing.T) {
	ctx := context.Background()
	h, store, _ := newTestHandler(t, ctx, 0, fetch.Permissive)
	srv := importSource(t, http.StatusForbidden)

	body, ctype := urlBody(t, srv.URL+"/post/", "gated")
	rec := post(h, body, ctype, "secret")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", rec.Code, rec.Body)
	}
	for _, want := range []string{
		`"error":"import_incomplete"`,
		`"url":"` + srv.URL + `/post/assets/hero.png"`,
		`"reason":"status 403"`,
		"Save the page in your browser",
	} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("missing %s in %s", want, rec.Body)
		}
	}
	if store.Len() != 0 {
		t.Fatalf("%d objects stored after a gated import, want 0", store.Len())
	}
	if rec2 := getMeta(h, "gated-1"); rec2.Code != http.StatusNotFound {
		t.Fatalf("gated slug status = %d, want 404", rec2.Code)
	}
}

// TestUploadBestEffortUnchanged: a file upload with an unfetchable asset is
// still 201 — the strict gate applies to imports only.
func TestUploadBestEffortUnchanged(t *testing.T) {
	ctx := context.Background()
	h, _, _ := newTestHandler(t, ctx, 0, fetch.Permissive)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	html := `<!doctype html><html><body><img src="` + deadURL + `/gone.png"></body></html>`
	body, ctype := multipartBody(t, "p.html", []byte(html), nil)
	rec := post(h, body, ctype, "secret")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201 (best-effort upload)", rec.Code, rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"kept_external":1`)) {
		t.Fatalf("missing kept_external in %s", rec.Body)
	}
}
