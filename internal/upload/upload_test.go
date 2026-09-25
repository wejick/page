//go:build integration

package upload

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	postgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"page/internal/config"
	"page/internal/db"
	"page/internal/ingest"
	"page/internal/storage/mem"
)

func newTestHandler(t *testing.T, ctx context.Context, rawCap int64) (*Handler, *mem.Store) {
	t.Helper()
	pgc, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("page"), postgres.WithUsername("page"),
		postgres.WithPassword("page"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })
	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if rawCap <= 0 {
		rawCap = 25 << 20
	}
	store := mem.New()
	h := New(Options{
		Pool:  pool,
		Store: store,
		Caps: config.Caps{
			MaxRawBytes: rawCap, MaxDecompressedBytes: 1 << 20,
			MaxFiles: 100, MaxAssetBytes: 1 << 20,
			FetchTimeout: time.Second, FetchBudget: 5 * time.Second, FetchConcurrency: 2,
		},
		Keep: ingest.KeepRules{
			Fonts: []string{"fonts.googleapis.com", "fonts.gstatic.com"},
			JS:    []string{"cdn.jsdelivr.net"},
		},
		Token: "secret",
	})
	return h, store
}

func multipartBody(t *testing.T, filename string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf, mw.FormDataContentType()
}

func post(h *Handler, body *bytes.Buffer, ctype, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/pages", body)
	req.Header.Set("Content-Type", ctype)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.Create().ServeHTTP(rec, req)
	return rec
}

func getMeta(h *Handler, slug string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/pages/"+slug, nil)
	req.SetPathValue("slug", slug)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.Get().ServeHTTP(rec, req)
	return rec
}

func mustTestZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestUploadAuth(t *testing.T) {
	ctx := context.Background()
	h, store := newTestHandler(t, ctx, 0)
	body, ctype := multipartBody(t, "p.html", []byte("<h1>x</h1>"), nil)

	if rec := post(h, body, ctype, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d, want 401", rec.Code)
	}
	body, ctype = multipartBody(t, "p.html", []byte("<h1>x</h1>"), nil)
	if rec := post(h, body, ctype, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", rec.Code)
	}
	if store.Len() != 0 {
		t.Fatal("unauthenticated upload stored objects")
	}

	body, ctype = multipartBody(t, "p.html", []byte("<h1>x</h1>"), nil)
	if rec := post(h, body, ctype, "secret"); rec.Code != http.StatusCreated {
		t.Fatalf("good token status = %d body=%s, want 201", rec.Code, rec.Body)
	}
}

func TestSingleHTMLHappyPath(t *testing.T) {
	ctx := context.Background()
	h, _ := newTestHandler(t, ctx, 0)
	body, ctype := multipartBody(t, "page.html", []byte("<h1>landing</h1>"),
		map[string]string{"identifier": "landing-page"})

	rec := post(h, body, ctype, "secret")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"slug":"landing-page-1"`)) {
		t.Fatalf("slug missing in %s", rec.Body)
	}

	// Second upload with same identifier increments.
	body2, ctype2 := multipartBody(t, "page.html", []byte("<h1>two</h1>"),
		map[string]string{"identifier": "landing-page"})
	rec2 := post(h, body2, ctype2, "secret")
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"slug":"landing-page-2"`)) {
		t.Fatalf("second slug wrong: %s", rec2.Body)
	}

	// Metadata endpoint.
	if rec3 := getMeta(h, "landing-page-1"); rec3.Code != 200 ||
		!bytes.Contains(rec3.Body.Bytes(), []byte(`"identifier":"landing-page"`)) {
		t.Fatalf("metadata status=%d body=%s", rec3.Code, rec3.Body)
	}
}

func TestIdentifierOmitted(t *testing.T) {
	ctx := context.Background()
	h, _ := newTestHandler(t, ctx, 0)
	body, ctype := multipartBody(t, "p.html", []byte("<h1>x</h1>"), nil)
	if rec := post(h, body, ctype, "secret"); rec.Code != 201 ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"slug":"page-1"`)) {
		t.Fatalf("default identifier status=%d body=%s, want 201 page-1", rec.Code, rec.Body)
	}
}

func TestZipPackManifest(t *testing.T) {
	ctx := context.Background()
	h, store := newTestHandler(t, ctx, 0)

	zipBytes := mustTestZip(t, map[string]string{
		"index.html":      `<html><head><link rel="stylesheet" href="style.css"></head><body><img src="assets/hero.png"></body></html>`,
		"style.css":       "body{color:red}",
		"assets/hero.png": "\x89PNG\r\n\x1a\n",
	})
	body, ctype := multipartBody(t, "pack.zip", zipBytes, map[string]string{"identifier": "pack"})
	rec := post(h, body, ctype, "secret")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	for _, want := range []string{`"slug":"pack-1"`, `"local":2`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("missing %s in %s", want, rec.Body)
		}
	}
	for _, k := range []string{"pack-1/index.html", "pack-1/style.css", "pack-1/assets/hero.png"} {
		if _, err := store.Stat(ctx, k); err != nil {
			t.Fatalf("object %s missing: %v", k, err)
		}
	}
	rec3 := getMeta(h, "pack-1")
	for _, want := range []string{`"path":"style.css"`, `"status":"local"`, `"asset_count":2`} {
		if !bytes.Contains(rec3.Body.Bytes(), []byte(want)) {
			t.Fatalf("missing %s in %s", want, rec3.Body)
		}
	}
}

func TestRejections(t *testing.T) {
	ctx := context.Background()
	h, store := newTestHandler(t, ctx, 0)

	cases := []struct {
		name    string
		file    string
		content []byte
		fields  map[string]string
		want    int
	}{
		{"no-html zip", "p.zip", mustTestZip(t, map[string]string{"a.png": "x"}), nil, 422},
		{"reserved identifier", "p.html", []byte("<h1>x</h1>"), map[string]string{"identifier": "api"}, 422},
		{"unsalvageable identifier", "p.html", []byte("<h1>x</h1>"), map[string]string{"identifier": "???"}, 422},
		{"not html or zip", "x.css", []byte("a{}"), nil, 415},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ctype := multipartBody(t, tc.file, tc.content, tc.fields)
			rec := post(h, body, ctype, "secret")
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body, tc.want)
			}
		})
	}

	// Traversal zip → 422.
	body, ctype := multipartBody(t, "p.zip", mustTestZip(t, map[string]string{"../evil.txt": "x"}), nil)
	if rec := post(h, body, ctype, "secret"); rec.Code != 422 {
		t.Fatalf("traversal status = %d, want 422", rec.Code)
	}
	// Oversize raw → 413.
	h2, _ := newTestHandler(t, ctx, 16)
	body2, ctype2 := multipartBody(t, "big.html", bytes.Repeat([]byte("x"), 64), nil)
	if rec := post(h2, body2, ctype2, "secret"); rec.Code != 413 {
		t.Fatalf("oversize status = %d, want 413", rec.Code)
	}
	// Nothing was stored anywhere across all rejections.
	if n := store.Len(); n != 0 {
		t.Errorf("%d objects stored after rejections, want 0", n)
	}
}
