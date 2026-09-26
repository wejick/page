//go:build integration

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

// TestManagementEndToEnd covers the management surface over the full stack:
// list (pagination, status filter), park reflected in the list, delete during
// a transition (409), and delete removing objects and rows end to end.
func TestManagementEndToEnd(t *testing.T) {
	ctx := context.Background()

	pg := startPostgres(t, ctx)
	pool := pg.Pool
	store, _ := startMinio(t, ctx)

	api := upload.New(upload.Options{
		Pool: pool, Store: store,
		Caps: config.Caps{
			MaxRawBytes: 25 << 20, MaxDecompressedBytes: 100 << 20,
			MaxFiles: 2000, MaxAssetBytes: 10 << 20,
			FetchTimeout: time.Second, FetchBudget: 5 * time.Second, FetchConcurrency: 4,
		},
		Keep:  ingest.KeepRules{},
		Token: "secret",
	})
	lc := lifecycle.New(pool, store)
	ts := httptest.NewServer(serve.New(serve.Options{
		Store: store, CacheTTL: 300 * time.Millisecond,
		Upload: api, Lifecycle: lifecycle.NewAPI(lc, "secret"), Ping: pool.Ping,
	}))
	t.Cleanup(ts.Close)

	authed := func(method, path string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	// Two pages: mgmt-1, then mgmt-2 (newest first in the list).
	for _, ident := range []string{"mgmt", "mgmt"} {
		mb, contentType := uploadBody(t, seedPack, ident)
		req, _ := http.NewRequest("POST", ts.URL+"/api/pages", mb)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload = %d body=%s", resp.StatusCode, body)
		}
	}

	// List: total 2, newest first, with url and status fields.
	resp, body := authed("GET", "/api/pages")
	if resp.StatusCode != 200 || !strings.Contains(body, `"total":2`) {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	first, second := strings.Index(body, `"slug":"mgmt-2"`), strings.Index(body, `"slug":"mgmt-1"`)
	if first < 0 || second < 0 || first > second {
		t.Fatalf("list order wrong: %s", body)
	}
	for _, want := range []string{`"url":"/p/mgmt-1/"`, `"status":"live"`, `"asset_count":3`} {
		if !strings.Contains(body, want) {
			t.Fatalf("list missing %s: %s", want, body)
		}
	}

	// Pagination window keeps the unfiltered total.
	_, body = authed("GET", "/api/pages?limit=1&offset=1")
	if !strings.Contains(body, `"total":2`) || !strings.Contains(body, `"slug":"mgmt-1"`) ||
		strings.Contains(body, `"slug":"mgmt-2"`) {
		t.Fatalf("window wrong: %s", body)
	}

	// Park mgmt-1; the list and the status filter follow.
	if r, b := authed("POST", "/api/pages/mgmt-1/park"); r.StatusCode != 200 ||
		!strings.Contains(b, `"status":"parked"`) {
		t.Fatalf("park = %d %s", r.StatusCode, b)
	}
	_, body = authed("GET", "/api/pages?status=parked")
	if !strings.Contains(body, `"total":1`) || !strings.Contains(body, `"slug":"mgmt-1"`) ||
		strings.Contains(body, `"slug":"mgmt-2"`) {
		t.Fatalf("parked filter wrong: %s", body)
	}

	// Delete during a lifecycle transition: 409, page untouched.
	if _, err := pool.Exec(ctx,
		`UPDATE pages SET status = 'parking' WHERE slug = 'mgmt-1'`); err != nil {
		t.Fatalf("force parking: %v", err)
	}
	if r, _ := authed("DELETE", "/api/pages/mgmt-1"); r.StatusCode != http.StatusConflict {
		t.Fatalf("delete during transition = %d, want 409", r.StatusCode)
	}

	// Settle to parked, then delete: objects and rows gone.
	if err := lc.Sweep(ctx); err != nil {
		t.Fatalf("sweep settle: %v", err)
	}
	if r, _ := authed("DELETE", "/api/pages/mgmt-1"); r.StatusCode != 200 {
		t.Fatalf("delete = %d, want 200", r.StatusCode)
	}

	// The parked copy lived under _parked/ — both prefixes must be gone.
	time.Sleep(700 * time.Millisecond) // entry-cache revalidation window
	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	for _, p := range []string{"/p/mgmt-1/", "/a/mgmt-1/assets/hero.png", "/p/mgmt-2/"} {
		req, _ := http.NewRequest("GET", ts.URL+p, nil)
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		r.Body.Close()
		want := 404
		if p == "/p/mgmt-2/" {
			want = 200 // the sibling survives the delete untouched
		}
		if r.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", p, r.StatusCode, want)
		}
	}

	// The deleted page is absent from the list; the sibling remains.
	_, body = authed("GET", "/api/pages")
	if !strings.Contains(body, `"total":1`) || strings.Contains(body, `"slug":"mgmt-1"`) {
		t.Fatalf("list after delete wrong: %s", body)
	}

	// Deleting it again: 404.
	if r, _ := authed("DELETE", "/api/pages/mgmt-1"); r.StatusCode != 404 {
		t.Fatalf("re-delete = %d, want 404", r.StatusCode)
	}

	// Unauthenticated list and delete are rejected before any I/O.
	req, _ := http.NewRequest("GET", ts.URL+"/api/pages", nil)
	if r, err := client.Do(req); err != nil || r.StatusCode != 401 {
		t.Fatalf("unauthenticated list = %d/%v, want 401", r.StatusCode, err)
	} else {
		r.Body.Close()
	}
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/pages/mgmt-2", nil)
	if r, err := client.Do(req); err != nil || r.StatusCode != 401 {
		t.Fatalf("unauthenticated delete = %d/%v, want 401", r.StatusCode, err)
	} else {
		r.Body.Close()
	}
}
