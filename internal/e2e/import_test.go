//go:build integration

package e2e

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"page/internal/config"
	"page/internal/fetch"
	"page/internal/ingest"
	"page/internal/serve"
	"page/internal/upload"
)

// TestImportByURLEndToEnd: import by URL through the API and serve the
// mirrored page plus its baked assets through the public router; then the
// strict gate rejects a partially fetchable source with nothing stored.
// (The file-upload regression journey is TestUploadAndServeEndToEnd.)
func TestImportByURLEndToEnd(t *testing.T) {
	ctx := context.Background()
	pg := startPostgres(t, ctx)
	store, _ := startMinio(t, ctx)

	// Source site: entry document with relative refs; the hero image's
	// status is toggled between the happy-path and strict-gate legs.
	var heroStatus atomic.Int32
	heroStatus.Store(http.StatusOK)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/post/":
			_, _ = w.Write([]byte(`<!doctype html><html><head>` +
				`<link rel="stylesheet" href="assets/style.css"></head>` +
				`<body><img src="assets/hero.png"></body></html>`))
		case "/post/assets/style.css":
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write([]byte("body{color:red}"))
		case "/post/assets/hero.png":
			if heroStatus.Load() != http.StatusOK {
				http.Error(w, "nope", int(heroStatus.Load()))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNG1"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)

	api := upload.New(upload.Options{
		Pool: pg.Pool, Store: store,
		Caps: config.Caps{
			MaxRawBytes: 25 << 20, MaxDecompressedBytes: 100 << 20,
			MaxFiles: 2000, MaxAssetBytes: 10 << 20,
			FetchTimeout: time.Second, FetchBudget: 5 * time.Second, FetchConcurrency: 4,
		},
		Keep:  ingest.KeepRules{Fonts: []string{"fonts.googleapis.com"}},
		Token: "secret",
		Guard: fetch.Permissive, // the source is a loopback httptest server
	})
	ts := httptest.NewServer(serve.New(serve.Options{
		Store: store, Upload: api, Ping: pg.Pool.Ping,
	}))
	t.Cleanup(ts.Close)

	importPost := func(url, identifier string) (*http.Response, string) {
		t.Helper()
		mb := &bytes.Buffer{}
		mw := multipart.NewWriter(mb)
		_ = mw.WriteField("url", url)
		_ = mw.WriteField("identifier", identifier)
		_ = mw.Close()
		req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/pages", mb)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /api/pages: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(ts.URL + path) //nolint — test client over httptest
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	// --- Happy path: the import mirrors the page, assets baked from source.
	resp, body := importPost(site.URL+"/post/", "e2eimp")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import status = %d body=%s, want 201", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"slug":"e2eimp-1"`) || !strings.Contains(body, `"baked":2`) {
		t.Fatalf("import response = %s", body)
	}

	gres, gbody := get("/p/e2eimp-1/")
	if gres.StatusCode != 200 {
		t.Fatalf("page status = %d", gres.StatusCode)
	}
	for _, want := range []string{
		`href="/a/e2eimp-1/external/127.0.0.1/post/assets/style.css"`,
		`src="/a/e2eimp-1/external/127.0.0.1/post/assets/hero.png"`,
	} {
		if !strings.Contains(gbody, want) {
			t.Errorf("page missing %q:\n%s", want, gbody)
		}
	}
	aresp, abody := get("/a/e2eimp-1/external/127.0.0.1/post/assets/hero.png")
	if aresp.StatusCode != 200 || abody != "PNG1" {
		t.Errorf("baked asset = %d %q", aresp.StatusCode, abody)
	}

	// --- Strict gate: one unfetchable asset rejects the whole import.
	heroStatus.Store(http.StatusForbidden)
	resp, body = importPost(site.URL+"/post/", "e2egate")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("gated import status = %d body=%s, want 422", resp.StatusCode, body)
	}
	for _, want := range []string{
		`"error":"import_incomplete"`,
		`"url":"` + site.URL + `/post/assets/hero.png"`,
		`"reason":"status 403"`,
		"Save the page in your browser",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("gate response missing %q:\n%s", want, body)
		}
	}
	// Nothing was published: no page, no assets, no manifest entry.
	for _, p := range []string{"/p/e2egate-1/", "/a/e2egate-1/index.html"} {
		if r, _ := get(p); r.StatusCode != 404 {
			t.Errorf("gated %s = %d, want 404", p, r.StatusCode)
		}
	}
	mreq, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/pages/e2egate-1", nil)
	mreq.Header.Set("Authorization", "Bearer secret")
	mresp, err := http.DefaultClient.Do(mreq)
	if err != nil {
		t.Fatalf("gated manifest: %v", err)
	}
	mresp.Body.Close()
	if mresp.StatusCode != 404 {
		t.Errorf("gated manifest = %d, want 404", mresp.StatusCode)
	}
}
