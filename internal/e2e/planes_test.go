//go:build integration

package e2e

import (
	"context"
	"encoding/json"
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
	"page/internal/storage/s3compat"
	"page/internal/upload"
)

// TestSplitPlanesServeAndAdmin runs the production split topology (ticket 6.1,
// deployment-modes D1/D3/D4): an admin-mode and an independent serve-mode
// instance up at once over one MinIO bucket, the serve instance wired from a
// storage-only env map exactly as runServe wires it. A park on the admin
// plane must reach the serve instance purely through key existence in the
// shared bucket.
func TestSplitPlanesServeAndAdmin(t *testing.T) {
	ctx := context.Background()

	// Postgres: the admin plane's dependency. It stays up for the whole test —
	// the database-terminated scenario belongs to
	// TestServeModeBootsWithoutDatabase; here both instances coexist.
	pool := startPostgres(t, ctx).Pool

	// MinIO: the one shared bucket both instances see.
	store, endpoint := startMinio(t, ctx)

	// --- Admin instance: the admin plane exactly as runAdminAll wires it.
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
	lc := lifecycle.New(pool, store)
	adminTS := httptest.NewServer(serve.New(serve.Options{
		Mode: config.ModeAdmin, Store: store,
		Upload: api, Lifecycle: lifecycle.NewAPI(lc, "secret"), Ping: pool.Ping,
	}))
	t.Cleanup(adminTS.Close)

	// --- Serve instance: a second, independent instance over the same
	// bucket, built the way runServe wires it. config.Load must accept this
	// env map precisely because serve mode does not require the database
	// variables it deliberately omits.
	serveEnv := map[string]string{
		"SERVER_MODE":    "serve",
		"STORAGE_DRIVER": "s3compat",
		"S3_ENDPOINT":    endpoint,
		"S3_BUCKET":      "pages",
		"S3_ACCESS_KEY":  "minioadmin",
		"S3_SECRET_KEY":  "minioadmin",
		"S3_PATH_STYLE":  "true",
	}
	cfg, err := config.Load(func(key string) string { return serveEnv[key] })
	if err != nil {
		t.Fatalf("serve-mode config: %v", err)
	}
	if cfg.Mode != config.ModeServe {
		t.Fatalf("mode = %q, want serve", cfg.Mode)
	}
	// Its own storage client and its own entry cache: sharing either with the
	// admin instance would make the park-propagation assertion meaningless.
	serveStore, err := s3compat.New(cfg.Storage)
	if err != nil {
		t.Fatalf("serve s3compat: %v", err)
	}
	// Short TTL so a park on the admin plane reaches this instance within the
	// test's sleep budget (entry HTML revalidates via Stat after the TTL).
	serveTS := httptest.NewServer(serve.New(serve.Options{
		Mode: cfg.Mode, Store: serveStore, CacheTTL: 100 * time.Millisecond,
		Ping: serve.StorageProbe(serveStore),
	}))
	t.Cleanup(serveTS.Close)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	call := func(base, method, path, token string, body io.Reader, contentType string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	// --- Seed a page through the admin instance's upload API.
	mb, contentType := uploadBody(t, seedPack, "planes")
	ures, ubody := call(adminTS.URL, "POST", "/api/pages", "secret", mb, contentType)
	if ures.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", ures.StatusCode, ubody)
	}
	var created struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(ubody), &created); err != nil || created.Slug == "" {
		t.Fatalf("upload body = %s (err=%v)", ubody, err)
	}
	slug := created.Slug

	// --- Serve-plane contract (deployment-modes D3): pages and assets serve,
	// nothing else.
	gres, gbody := call(serveTS.URL, "GET", "/p/"+slug+"/", "", nil, "")
	if gres.StatusCode != http.StatusOK {
		t.Fatalf("serve page status = %d body=%s", gres.StatusCode, gbody)
	}
	for _, want := range []string{
		`href="/a/` + slug + `/style.css"`,
		`src="/a/` + slug + `/assets/hero.png"`,
	} {
		if !strings.Contains(gbody, want) {
			t.Errorf("serve page missing %q", want)
		}
	}
	for _, p := range []string{"/a/" + slug + "/assets/hero.png", "/p/" + slug + "/assets/hero.png"} {
		if r, b := call(serveTS.URL, "GET", p, "", nil, ""); r.StatusCode != http.StatusOK || string(b) != "HERO" {
			t.Errorf("serve %s = %d %q", p, r.StatusCode, b)
		}
	}

	// Nothing admin is mounted on the serve plane — not even with the admin
	// token: the routes are absent, not merely unauthorized.
	for _, p := range []string{"/", "/api/pages/" + slug} {
		if r, _ := call(serveTS.URL, "GET", p, "secret", nil, ""); r.StatusCode != http.StatusNotFound {
			t.Errorf("serve GET %s = %d, want 404", p, r.StatusCode)
		}
	}
	if r, _ := call(serveTS.URL, "POST", "/api/pages", "secret", nil, ""); r.StatusCode != http.StatusNotFound {
		t.Errorf("serve POST /api/pages = %d, want 404", r.StatusCode)
	}
	for _, p := range []string{"/api/pages/" + slug + "/park", "/api/pages/" + slug + "/unpark"} {
		if r, _ := call(serveTS.URL, "POST", p, "secret", nil, ""); r.StatusCode != http.StatusNotFound {
			t.Errorf("serve POST %s = %d, want 404", p, r.StatusCode)
		}
	}
	// Serve health probes storage only (deployment-modes D4): 200 with no
	// database configured.
	if r, _ := call(serveTS.URL, "GET", "/healthz", "", nil, ""); r.StatusCode != http.StatusOK {
		t.Errorf("serve healthz = %d, want 200", r.StatusCode)
	}

	// --- Admin-plane contract: upload UI + API mounted, page serving absent.
	if r, _ := call(adminTS.URL, "GET", "/", "", nil, ""); r.StatusCode != http.StatusOK {
		t.Errorf("admin GET / = %d, want 200 (upload UI)", r.StatusCode)
	}
	for _, p := range []string{"/p/" + slug + "/", "/a/" + slug + "/assets/hero.png"} {
		if r, _ := call(adminTS.URL, "GET", p, "", nil, ""); r.StatusCode != http.StatusNotFound {
			t.Errorf("admin GET %s = %d, want 404", p, r.StatusCode)
		}
	}
	if r, _ := call(adminTS.URL, "GET", "/healthz", "", nil, ""); r.StatusCode != http.StatusOK {
		t.Errorf("admin healthz = %d, want 200", r.StatusCode)
	}

	// The admin manifest answers with the lifecycle status (upload.Get's JSON
	// shape: "slug" and "status" among the fields).
	fetchStatus := func() string {
		t.Helper()
		r, b := call(adminTS.URL, "GET", "/api/pages/"+slug, "secret", nil, "")
		if r.StatusCode != http.StatusOK {
			t.Fatalf("admin manifest = %d %s", r.StatusCode, b)
		}
		var manifest struct {
			Slug   string `json:"slug"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(b), &manifest); err != nil {
			t.Fatalf("manifest body = %s: %v", b, err)
		}
		if manifest.Slug != slug {
			t.Fatalf("manifest slug = %q, want %q", manifest.Slug, slug)
		}
		return manifest.Status
	}
	if status := fetchStatus(); status != lifecycle.StatusLive {
		t.Errorf("manifest status = %q, want %q", status, lifecycle.StatusLive)
	}

	// --- Park on the admin plane: the serve instance must converge within its
	// revalidation TTL, learning the state only from key existence in the
	// shared bucket (its storage client and cache are its own).
	pr, pbody := call(adminTS.URL, "POST", "/api/pages/"+slug+"/park", "secret", nil, "")
	if pr.StatusCode != http.StatusOK || !strings.Contains(pbody, `"status":"parked"`) {
		t.Fatalf("park = %d %s", pr.StatusCode, pbody)
	}
	if status := fetchStatus(); status != lifecycle.StatusParked {
		t.Errorf("manifest status after park = %q, want %q", status, lifecycle.StatusParked)
	}
	time.Sleep(500 * time.Millisecond) // > the serve instance's 100ms TTL
	for _, p := range []string{"/p/" + slug + "/", "/a/" + slug + "/assets/hero.png"} {
		if r, _ := call(serveTS.URL, "GET", p, "", nil, ""); r.StatusCode != http.StatusNotFound {
			t.Errorf("parked serve %s = %d, want 404", p, r.StatusCode)
		}
	}

	// --- Unpark restores the page on both planes, status back to live.
	ur, _ := call(adminTS.URL, "POST", "/api/pages/"+slug+"/unpark", "secret", nil, "")
	if ur.StatusCode != http.StatusOK {
		t.Fatalf("unpark = %d", ur.StatusCode)
	}
	if r, _ := call(serveTS.URL, "GET", "/p/"+slug+"/", "", nil, ""); r.StatusCode != http.StatusOK {
		t.Errorf("unparked serve page = %d, want 200", r.StatusCode)
	}
	if status := fetchStatus(); status != lifecycle.StatusLive {
		t.Errorf("manifest status after unpark = %q, want %q", status, lifecycle.StatusLive)
	}
}
