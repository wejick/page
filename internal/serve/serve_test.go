package serve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"page/internal/config"
	"page/internal/lifecycle"
	"page/internal/storage"
	"page/internal/storage/mem"
	"page/internal/upload"
)

const testToken = "test-token-1"

// countingStore counts Get/Stat calls to prove the cache absorbs repeat
// views and that revalidation uses Stat, not byte refetches.
type countingStore struct {
	storage.Storage
	gets  int
	stats int
}

func (c *countingStore) Get(ctx context.Context, key string) (storage.Object, error) {
	c.gets++
	return c.Storage.Get(ctx, key)
}

func (c *countingStore) Stat(ctx context.Context, key string) (storage.ObjectMeta, error) {
	c.stats++
	return c.Storage.Stat(ctx, key)
}

func newTestHandler(t *testing.T, seed map[string][2]string) (*httptest.Server, *countingStore) {
	t.Helper()
	return newTestHandlerOpts(t, seed, Options{})
}

func newTestHandlerOpts(t *testing.T, seed map[string][2]string, o Options) (*httptest.Server, *countingStore) {
	t.Helper()
	memStore := mem.New()
	for key, v := range seed {
		if err := memStore.Put(context.Background(), key, v[0], strings.NewReader(v[1])); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	cs := &countingStore{Storage: memStore}
	o.Store = cs
	ts := httptest.NewServer(New(o))
	t.Cleanup(ts.Close)
	return ts, cs
}

func TestRoutes(t *testing.T) {
	ts, _ := newTestHandler(t, map[string][2]string{
		"s-1/index.html":       {"text/html; charset=utf-8", "<h1>hi</h1>"},
		"s-1/assets/hero.png":  {"image/png", "PNGDATA"},
		"other-2/docs/deep.js": {"text/javascript", "console.log(1)"},
	})
	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // do not follow
	}

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantLoc    string
		wantBody   string
		wantCT     string
	}{
		{name: "page index", path: "/p/s-1/", wantStatus: 200, wantBody: "<h1>hi</h1>", wantCT: "text/html; charset=utf-8"},
		{name: "slashless redirects", path: "/p/s-1", wantStatus: 301, wantLoc: "/p/s-1/"},
		{name: "asset via /p dual-mount", path: "/p/s-1/assets/hero.png", wantStatus: 200, wantBody: "PNGDATA", wantCT: "image/png"},
		{name: "asset via /a canonical", path: "/a/s-1/assets/hero.png", wantStatus: 200, wantBody: "PNGDATA", wantCT: "image/png"},
		{name: "nested asset path", path: "/a/other-2/docs/deep.js", wantStatus: 200, wantBody: "console.log(1)", wantCT: "text/javascript"},
		{name: "unknown slug page", path: "/p/does-not-exist-9/", wantStatus: 404},
		{name: "unknown slug asset", path: "/a/does-not-exist-9/x.png", wantStatus: 404},
		{name: "missing asset on known page", path: "/a/s-1/assets/gone.png", wantStatus: 404},
		{name: "empty rest rejected", path: "/a/s-1/", wantStatus: 404},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := client.Get(ts.URL + tt.path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantLoc != "" && resp.Header.Get("Location") != tt.wantLoc {
				t.Fatalf("Location = %q, want %q", resp.Header.Get("Location"), tt.wantLoc)
			}
			if tt.wantCT != "" && resp.Header.Get("Content-Type") != tt.wantCT {
				t.Fatalf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), tt.wantCT)
			}
			if tt.wantBody != "" {
				body, _ := io.ReadAll(resp.Body)
				if string(body) != tt.wantBody {
					t.Fatalf("body = %q, want %q", body, tt.wantBody)
				}
			}
		})
	}
}

// TestPlaneScopedRouting asserts, per mode, which surfaces New mounts
// (deployment-modes). Mounted admin endpoints answer 401 to unauthenticated
// requests; unmounted ones 404. Serve builds with nil Upload/Lifecycle (they
// are never constructed there); admin and all carry both.
func TestPlaneScopedRouting(t *testing.T) {
	seed := map[string][2]string{
		"s-1/index.html":      {"text/html; charset=utf-8", "<h1>hi</h1>"},
		"s-1/assets/hero.png": {"image/png", "PNGDATA"},
	}
	newOpts := func(mode config.Mode) Options {
		o := Options{Mode: mode}
		if mode != config.ModeServe {
			o.Upload = upload.New(upload.Options{Token: testToken})
			o.Lifecycle = lifecycle.NewAPI(lifecycle.New(nil, nil), testToken)
		}
		return o
	}

	tests := []struct {
		name       string
		mode       config.Mode
		method     string
		path       string
		wantStatus int
	}{
		// serve: /p/*, /a/*, /healthz only.
		{name: "serve: entry page", mode: config.ModeServe, path: "/p/s-1/", wantStatus: 200},
		{name: "serve: slashless redirect", mode: config.ModeServe, path: "/p/s-1", wantStatus: 301},
		{name: "serve: asset via /a", mode: config.ModeServe, path: "/a/s-1/assets/hero.png", wantStatus: 200},
		{name: "serve: healthz", mode: config.ModeServe, path: "/healthz", wantStatus: 200},
		{name: "serve: no upload UI", mode: config.ModeServe, path: "/", wantStatus: 404},
		{name: "serve: no upload API", mode: config.ModeServe, method: http.MethodPost, path: "/api/pages", wantStatus: 404},
		{name: "serve: no page status API", mode: config.ModeServe, path: "/api/pages/s-1", wantStatus: 404},
		{name: "serve: no page list API", mode: config.ModeServe, path: "/api/pages", wantStatus: 404},
		{name: "serve: no park", mode: config.ModeServe, method: http.MethodPost, path: "/api/pages/s-1/park", wantStatus: 404},
		{name: "serve: no delete", mode: config.ModeServe, method: http.MethodDelete, path: "/api/pages/s-1", wantStatus: 404},

		// admin: /, /api/*, /healthz only.
		{name: "admin: upload UI", mode: config.ModeAdmin, path: "/", wantStatus: 200},
		{name: "admin: healthz", mode: config.ModeAdmin, path: "/healthz", wantStatus: 200},
		{name: "admin: upload API mounted", mode: config.ModeAdmin, method: http.MethodPost, path: "/api/pages", wantStatus: 401},
		{name: "admin: page status API mounted", mode: config.ModeAdmin, path: "/api/pages/s-1", wantStatus: 401},
		{name: "admin: page list API mounted", mode: config.ModeAdmin, path: "/api/pages", wantStatus: 401},
		{name: "admin: park mounted", mode: config.ModeAdmin, method: http.MethodPost, path: "/api/pages/s-1/park", wantStatus: 401},
		{name: "admin: unpark mounted", mode: config.ModeAdmin, method: http.MethodPost, path: "/api/pages/s-1/unpark", wantStatus: 401},
		{name: "admin: delete mounted", mode: config.ModeAdmin, method: http.MethodDelete, path: "/api/pages/s-1", wantStatus: 401},
		{name: "admin: no page serving", mode: config.ModeAdmin, path: "/p/s-1/", wantStatus: 404},
		{name: "admin: no asset serving", mode: config.ModeAdmin, path: "/a/s-1/assets/hero.png", wantStatus: 404},
		{name: "admin: no slashless redirect", mode: config.ModeAdmin, path: "/p/s-1", wantStatus: 404},

		// all: everything, as today.
		{name: "all: upload UI", mode: config.ModeAll, path: "/", wantStatus: 200},
		{name: "all: entry page", mode: config.ModeAll, path: "/p/s-1/", wantStatus: 200},
		{name: "all: asset", mode: config.ModeAll, path: "/a/s-1/assets/hero.png", wantStatus: 200},
		{name: "all: healthz", mode: config.ModeAll, path: "/healthz", wantStatus: 200},
		{name: "all: upload API mounted", mode: config.ModeAll, method: http.MethodPost, path: "/api/pages", wantStatus: 401},
		{name: "all: page list API mounted", mode: config.ModeAll, path: "/api/pages", wantStatus: 401},
		{name: "all: park mounted", mode: config.ModeAll, method: http.MethodPost, path: "/api/pages/s-1/park", wantStatus: 401},
		{name: "all: delete mounted", mode: config.ModeAll, method: http.MethodDelete, path: "/api/pages/s-1", wantStatus: 401},

		// The zero-value Mode behaves as all.
		{name: "zero value: upload UI", mode: config.Mode(""), path: "/", wantStatus: 200},
		{name: "zero value: entry page", mode: config.Mode(""), path: "/p/s-1/", wantStatus: 200},
		{name: "zero value: upload API mounted", mode: config.Mode(""), method: http.MethodPost, path: "/api/pages", wantStatus: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := newTestHandlerOpts(t, seed, newOpts(tt.mode))
			client := ts.Client()
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // do not follow
			}
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req, err := http.NewRequest(method, ts.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, tt.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("%s %s: status = %d, want %d", method, tt.path, resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

// probeStore is an injectable store for the health-probe tests: Stat
// delegates to the mem store unless fail is set, which simulates a storage
// transport outage (deployment-modes: serve instance detects storage
// failure). gotKey records the last probed key.
type probeStore struct {
	*mem.Store
	fail   error
	gotKey string
}

func (p *probeStore) Stat(ctx context.Context, key string) (storage.ObjectMeta, error) {
	p.gotKey = key
	if p.fail != nil {
		return storage.ObjectMeta{}, p.fail
	}
	return p.Store.Stat(ctx, key)
}

func TestStorageProbe(t *testing.T) {
	t.Run("absent probe key is healthy (ErrNotFound proves the round-trip)", func(t *testing.T) {
		if err := StorageProbe(mem.New())(context.Background()); err != nil {
			t.Fatalf("probe on empty store = %v, want nil", err)
		}
	})
	t.Run("present probe key is healthy", func(t *testing.T) {
		s := mem.New()
		if err := s.Put(context.Background(), healthProbeKey, "text/plain", strings.NewReader("ok")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := StorageProbe(s)(context.Background()); err != nil {
			t.Fatalf("probe on existing key = %v, want nil", err)
		}
	})
	t.Run("transport failure is unhealthy", func(t *testing.T) {
		want := errors.New("dial tcp 10.0.0.1:9000: connection refused")
		err := StorageProbe(&probeStore{Store: mem.New(), fail: want})(context.Background())
		if !errors.Is(err, want) {
			t.Fatalf("probe on failing store = %v, want %v", err, want)
		}
	})
	t.Run("stats the fixed probe key", func(t *testing.T) {
		ps := &probeStore{Store: mem.New()}
		if err := StorageProbe(ps)(context.Background()); err != nil {
			t.Fatalf("probe: %v", err)
		}
		if ps.gotKey != healthProbeKey {
			t.Fatalf("probed key = %q, want %q", ps.gotKey, healthProbeKey)
		}
	})
}

// TestServeModeHealthzProbesStorage wires StorageProbe into a serve-mode
// router and asserts the health surface end-to-end: 200 while storage
// answers — the probe key is absent, so ErrNotFound is the healthy common
// case — and 503 when the storage transport fails.
func TestServeModeHealthzProbesStorage(t *testing.T) {
	ps := &probeStore{Store: mem.New()}
	ts := httptest.NewServer(New(Options{Store: ps, Mode: config.ModeServe, Ping: StorageProbe(ps)}))
	defer ts.Close()

	status := func() int {
		resp, err := http.Get(ts.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := status(); code != http.StatusOK {
		t.Fatalf("healthy storage status = %d, want 200", code)
	}

	ps.fail = errors.New("dial tcp: connection refused")
	if code := status(); code != http.StatusServiceUnavailable {
		t.Fatalf("failing storage status = %d, want 503", code)
	}
}

func TestTraversalNeverServes(t *testing.T) {
	// Traversal attempts may be redirected (mux cleans paths) or rejected,
	// but must never serve bytes outside the page prefix.
	ts, _ := newTestHandler(t, map[string][2]string{
		"s-1/index.html": {"text/html", "<h1>hi</h1>"},
	})
	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	for _, p := range []string{
		"/a/s-1/../secret",
		"/a/s-1/%2e%2e/secret",
		"/p/s-1/%2e%2e",
		"/a/s-1/assets/../../secret",
	} {
		resp, err := client.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("traversal %s returned 200", p)
		}
	}
}

func TestCacheHeadersSplitByPlane(t *testing.T) {
	ts, _ := newTestHandler(t, map[string][2]string{
		"s-1/index.html":      {"text/html; charset=utf-8", "<h1>hi</h1>"},
		"s-1/assets/hero.png": {"image/png", "PNGDATA"},
	})

	// Entry HTML: revalidatable, not immutable.
	resp, err := http.Get(ts.URL + "/p/s-1/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if resp.Header.Get("Cache-Control") != "public, max-age=60, must-revalidate" {
		t.Fatalf("entry Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff = %q", resp.Header.Get("X-Content-Type-Options"))
	}
	resp.Body.Close()

	// Assets: immutable.
	aresp, err := http.Get(ts.URL + "/a/s-1/assets/hero.png")
	if err != nil {
		t.Fatalf("asset GET: %v", err)
	}
	defer aresp.Body.Close()
	if aresp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", aresp.Header.Get("Cache-Control"))
	}
	if aresp.Header.Get("ETag") == "" {
		t.Fatal("asset missing ETag")
	}

	// Conditional request on the entry returns 304.
	req, _ := http.NewRequest("GET", ts.URL+"/p/s-1/", nil)
	req.Header.Set("If-None-Match", etag)
	resp304, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("conditional GET: %v", err)
	}
	defer resp304.Body.Close()
	if resp304.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional status = %d, want 304", resp304.StatusCode)
	}
}

func TestHTMLCacheAbsorbsRepeatViews(t *testing.T) {
	ts, cs := newTestHandler(t, map[string][2]string{
		"s-1/index.html": {"text/html; charset=utf-8", "<h1>hi</h1>"},
	})

	for i := 0; i < 3; i++ {
		resp, err := http.Get(ts.URL + "/p/s-1/")
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %d status = %d", i, resp.StatusCode)
		}
	}
	if cs.gets != 1 {
		t.Fatalf("storage Gets = %d after 3 views, want 1", cs.gets)
	}
}

func TestEntryCacheRevalidatesAfterTTL(t *testing.T) {
	ts, cs := newTestHandlerOpts(t, map[string][2]string{
		"s-1/index.html": {"text/html; charset=utf-8", "<h1>hi</h1>"},
	}, Options{CacheTTL: 30 * time.Millisecond})

	// First view: full fetch, no revalidation.
	resp, err := http.Get(ts.URL + "/p/s-1/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if cs.gets != 1 || cs.stats != 0 {
		t.Fatalf("after first view: gets=%d stats=%d, want 1/0", cs.gets, cs.stats)
	}

	// Within the TTL: served from memory, zero storage roundtrips.
	time.Sleep(10 * time.Millisecond)
	resp, err = http.Get(ts.URL + "/p/s-1/")
	if err != nil {
		t.Fatalf("GET within TTL: %v", err)
	}
	resp.Body.Close()
	if cs.gets != 1 || cs.stats != 0 {
		t.Fatalf("within TTL: gets=%d stats=%d, want 1/0", cs.gets, cs.stats)
	}

	// Past the TTL: revalidated via Stat only — bytes still served from
	// memory (no refetch).
	time.Sleep(40 * time.Millisecond)
	resp, err = http.Get(ts.URL + "/p/s-1/")
	if err != nil {
		t.Fatalf("GET after TTL: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("after TTL status = %d, want 200", resp.StatusCode)
	}
	if cs.gets != 1 {
		t.Fatalf("Gets after TTL = %d, want 1 (revalidate via Stat, not refetch)", cs.gets)
	}
	if cs.stats < 1 {
		t.Fatalf("Stats after TTL = %d, want >= 1", cs.stats)
	}
}

func TestParkedPageServes404AfterTTL(t *testing.T) {
	memStore := mem.New()
	cs := &countingStore{Storage: memStore}
	ts := httptest.NewServer(New(Options{Store: cs, CacheTTL: 30 * time.Millisecond}))
	defer ts.Close()

	if err := memStore.Put(context.Background(), "s-1/index.html", "text/html; charset=utf-8",
		strings.NewReader("<h1>hi</h1>")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	resp, err := http.Get(ts.URL + "/p/s-1/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("pre-park status = %d err = %v, want 200", resp.StatusCode, err)
	}
	resp.Body.Close()

	// Park: the objects vanish from storage.
	if err := memStore.DeletePrefix(context.Background(), "s-1/"); err != nil {
		t.Fatalf("DeletePrefix (park): %v", err)
	}

	// Within the TTL the cached bytes still serve (bounded, not instant).
	resp, err = http.Get(ts.URL + "/p/s-1/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("within-TTL status = %d err = %v, want 200 (cached)", resp.StatusCode, err)
	}
	resp.Body.Close()

	// Past the TTL revalidation sees the park: 404, entry evicted.
	time.Sleep(60 * time.Millisecond)
	resp, err = http.Get(ts.URL + "/p/s-1/")
	if err != nil {
		t.Fatalf("after-TTL GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("after-TTL status = %d, want 404 (parked)", resp.StatusCode)
	}

	// The eviction sticks: no resurrected bytes on the next view.
	resp, err = http.Get(ts.URL + "/p/s-1/")
	if err != nil || resp.StatusCode != 404 {
		t.Fatalf("post-eviction status = %d err = %v, want 404", resp.StatusCode, err)
	}
	resp.Body.Close()
}

func TestNoNegativeCaching(t *testing.T) {
	memStore := mem.New()
	cs := &countingStore{Storage: memStore}
	ts := httptest.NewServer(New(Options{Store: cs}))
	defer ts.Close()

	// Miss before the page exists.
	if resp, err := http.Get(ts.URL + "/p/fresh-1/"); err != nil || resp.StatusCode != 404 {
		t.Fatalf("pre-upload status = %d err = %v, want 404", resp.StatusCode, err)
	}
	// Upload after the miss.
	if err := memStore.Put(context.Background(), "fresh-1/index.html", "text/html; charset=utf-8",
		strings.NewReader("new")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Next request must serve the fresh page.
	resp, err := http.Get(ts.URL + "/p/fresh-1/")
	if err != nil {
		t.Fatalf("post-upload GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("post-upload status = %d, want 200", resp.StatusCode)
	}
}

func TestPathValidation(t *testing.T) {
	// Slug must be one clean segment; rest must not traverse.
	for _, s := range []string{"s-1", "landing-page-12"} {
		if !validSlugSegment(s) {
			t.Fatalf("validSlugSegment(%q) = false", s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`} {
		if validSlugSegment(s) {
			t.Fatalf("validSlugSegment(%q) = true, want false", s)
		}
	}
	for _, r := range []string{"assets/x.png", "deep/dir/file.js", "x"} {
		if !validRest(r) {
			t.Fatalf("validRest(%q) = false", r)
		}
	}
	for _, r := range []string{"", "..", "a/../b", "./x", "a//b"} {
		if validRest(r) {
			t.Fatalf("validRest(%q) = true, want false", r)
		}
	}
}
