//go:build integration

// Package e2e exercises the whole service against real MinIO + Postgres:
// upload a Framer-style zip through the API, then serve it through the
// public router (dual-mount, redirects, 404s) — the same URL contract the
// production CDN must satisfy (design D14).
package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"page/internal/config"
	"page/internal/ingest"
	"page/internal/lifecycle"
	"page/internal/serve"
	"page/internal/upload"
)

func TestUploadAndServeEndToEnd(t *testing.T) {
	ctx := context.Background()

	pg := startPostgres(t, ctx)
	pool := pg.Pool
	store, _ := startMinio(t, ctx)

	// The full router over real storage.
	api := upload.New(upload.Options{
		Pool: pool, Store: store,
		Caps: config.Caps{
			MaxRawBytes: 25 << 20, MaxDecompressedBytes: 100 << 20,
			MaxFiles: 2000, MaxAssetBytes: 10 << 20,
			FetchTimeout: time.Second, FetchBudget: 5 * time.Second, FetchConcurrency: 4,
		},
		Keep: ingest.KeepRules{
			Fonts: []string{"fonts.googleapis.com", "fonts.gstatic.com"},
			JS:    []string{"cdn.jsdelivr.net"},
		},
		Token: "secret",
	})

	// Short TTL so the park-propagation window is testable in seconds.
	lc := lifecycle.New(pool, store)
	ts := httptest.NewServer(serve.New(serve.Options{
		Store: store, CacheTTL: 300 * time.Millisecond,
		Upload: api, Lifecycle: lifecycle.NewAPI(lc, "secret"), Ping: pool.Ping,
	}))
	t.Cleanup(ts.Close)

	// --- Upload a Framer-style pack.
	pack := map[string]string{
		"index.html": `<!doctype html><html><head>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter">
<link rel="stylesheet" href="style.css">
<script src="https://cdn.jsdelivr.net/npm/app.js"></script>
</head><body>
<img src="assets/hero.png">
<img src="http://127.0.0.1:1/unreachable.png">
</body></html>`,
		"style.css":       "body{background:url(assets/bg.png)}",
		"assets/hero.png": "HERO",
		"assets/bg.png":   "BG",
	}
	mb, contentType := uploadBody(t, pack, "e2e")

	req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/pages", mb)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"slug":"e2e-1"`) {
		t.Fatalf("slug missing: %s", body)
	}

	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	get := func(path string) (*http.Response, string) {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	// Page serves, rewritten, with per-plane cache headers.
	gres, gbody := get("/p/e2e-1/")
	if gres.StatusCode != 200 {
		t.Fatalf("page status = %d", gres.StatusCode)
	}
	if gres.Header.Get("Cache-Control") != "public, max-age=60, must-revalidate" {
		t.Fatalf("entry cache-control = %q", gres.Header.Get("Cache-Control"))
	}
	for _, want := range []string{
		`href="/a/e2e-1/style.css"`,
		`src="/a/e2e-1/assets/hero.png"`,
		`src="https://127.0.0.1:1/unreachable.png"`, // kept-external, upgraded
		"https://fonts.googleapis.com/css2?family=Inter",
	} {
		if !strings.Contains(gbody, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// Slashless redirect.
	resp, _ = get("/p/e2e-1")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/p/e2e-1/" {
		t.Fatalf("slashless = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Assets resolve via BOTH prefixes with identical bytes.
	for _, p := range []string{"/a/e2e-1/assets/hero.png", "/p/e2e-1/assets/hero.png"} {
		resp, b := get(p)
		if resp.StatusCode != 200 || string(b) != "HERO" {
			t.Errorf("%s = %d %q", p, resp.StatusCode, b)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s content-type = %q", p, ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("%s cache-control = %q", p, cc)
		}
	}

	// The CSS file was rewritten: its local bg ref now points at /a/.
	resp, css := get("/a/e2e-1/style.css")
	if resp.StatusCode != 200 || !strings.Contains(css, "url(/a/e2e-1/assets/bg.png)") {
		t.Errorf("css = %d %q", resp.StatusCode, css)
	}

	// Missing things are 404.
	for _, p := range []string{"/p/missing-9/", "/a/e2e-1/assets/gone.png", "/api/pages/missing-9"} {
		req, _ := http.NewRequest("GET", ts.URL+p, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}

	// Repeat page views are absorbed by the html cache — asserted indirectly:
	// 10 further views must all succeed identically.
	for i := 0; i < 10; i++ {
		resp, b := get("/p/e2e-1/")
		if resp.StatusCode != 200 || !strings.Contains(b, "e2e-1/style.css") {
			t.Fatalf("cached view %d = %d", i, resp.StatusCode)
		}
	}

	// --- Manifest reports lifecycle status.
	req, _ = http.NewRequest("GET", ts.URL+"/api/pages/e2e-1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	mresp, err := client.Do(req)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	mbody, _ := io.ReadAll(mresp.Body)
	mresp.Body.Close()
	if mresp.StatusCode != 200 || !strings.Contains(string(mbody), `"status":"live"`) {
		t.Fatalf("manifest = %d %s, want live status", mresp.StatusCode, mbody)
	}

	// --- Park: entry, assets, and manifest all reflect the hidden state.
	entryETag := gres.Header.Get("ETag")
	parkPost := func(path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	presp := parkPost("/api/pages/e2e-1/park")
	prespBody, _ := io.ReadAll(presp.Body)
	presp.Body.Close()
	if presp.StatusCode != 200 || !strings.Contains(string(prespBody), `"status":"parked"`) {
		t.Fatalf("park = %d %s", presp.StatusCode, prespBody)
	}
	// Re-park is idempotent.
	presp = parkPost("/api/pages/e2e-1/park")
	if presp.StatusCode != 200 {
		t.Fatalf("re-park = %d, want 200", presp.StatusCode)
	}
	presp.Body.Close()

	// Parked pages must be fully gone once the revalidation TTL passes:
	// entry and both asset mounts.
	time.Sleep(700 * time.Millisecond)
	for _, p := range []string{"/p/e2e-1/", "/a/e2e-1/assets/hero.png", "/p/e2e-1/assets/hero.png"} {
		if r, _ := get(p); r.StatusCode != 404 {
			t.Fatalf("parked %s = %d, want 404", p, r.StatusCode)
		}
	}

	// --- Unpark: same URL, identical bytes, stable ETag.
	uresp := parkPost("/api/pages/e2e-1/unpark")
	if uresp.StatusCode != 200 {
		t.Fatalf("unpark = %d", uresp.StatusCode)
	}
	uresp.Body.Close()
	if r, b := get("/p/e2e-1/"); r.StatusCode != 200 || !strings.Contains(b, "e2e-1/style.css") {
		t.Fatalf("unparked page = %d %q", r.StatusCode, b)
	}
	// The pre-park ETag still revalidates to 304 — bytes never changed.
	req, _ = http.NewRequest("GET", ts.URL+"/p/e2e-1/", nil)
	req.Header.Set("If-None-Match", entryETag)
	cresp, err := client.Do(req)
	if err != nil {
		t.Fatalf("conditional after unpark: %v", err)
	}
	cresp.Body.Close()
	if cresp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional after unpark = %d, want 304 (etag must be stable)", cresp.StatusCode)
	}

	// Concurrent toggles converge: parallel park/unpark cannot corrupt state.
	done := make(chan int, 4)
	for i, path := range []string{
		"/api/pages/e2e-1/park", "/api/pages/e2e-1/unpark",
		"/api/pages/e2e-1/park", "/api/pages/e2e-1/unpark",
	} {
		go func(i int, path string) {
			resp := parkPost(path)
			defer resp.Body.Close()
			if resp.StatusCode != 200 && resp.StatusCode != http.StatusConflict {
				t.Errorf("concurrent %s = %d, want 200 or 409", path, resp.StatusCode)
			}
			done <- i
		}(i, path)
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	// Whatever the interleaving was, a final toggle lands on a consistent,
	// fully-moved state: park everything, then verify the contract.
	if r := parkPost("/api/pages/e2e-1/park"); r.StatusCode != 200 {
		t.Fatalf("settle park = %d", r.StatusCode)
	} else {
		r.Body.Close()
	}
	time.Sleep(700 * time.Millisecond)
	if r, _ := get("/p/e2e-1/"); r.StatusCode != 404 {
		t.Fatalf("settled parked page = %d, want 404", r.StatusCode)
	}
}
