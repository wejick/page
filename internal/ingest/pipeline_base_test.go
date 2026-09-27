package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"page/internal/fetch"
)

// baseTestServer serves assets for base-resolution tests.
func baseTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/post/assets/hero.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PNG1"))
	})
	mux.HandleFunc("/post/assets/hero@2x.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PNG2"))
	})
	mux.HandleFunc("/cdn/img.png", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("CDNPNG"))
	})
	mux.HandleFunc("/post/missing.png", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func basePipeline() *Pipeline {
	return &Pipeline{
		Keep: KeepRules{Fonts: []string{"fonts.googleapis.com"}},
		Fetch: NewFetcher(1<<20, 500*time.Millisecond, 3*time.Second, 4,
			fetch.Permissive),
	}
}

// TestProcessWithBase covers URL-import resolution: relative refs resolve
// against the base and flow through the existing classify path.
func TestProcessWithBase(t *testing.T) {
	srv := baseTestServer(t)
	base := mustURL(t, srv.URL+"/post/")

	// An import's files map holds only the entry document.
	files := map[string][]byte{
		"index.html": []byte(`<!doctype html><html><head>` +
			`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter">` +
			`</head><body>` +
			`<img src="assets/hero.png" srcset="assets/hero.png 1x, assets/hero@2x.png 2x">` +
			`<img src="missing.png">` +
			`<img src="data:image/gif;base64,R0lGODlh">` +
			`</body></html>`),
	}
	res, err := basePipeline().Process(context.Background(), files, "index.html", "imp-1", base)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	entry := string(res.Entry)
	for _, want := range []string{
		`/a/imp-1/external/127.0.0.1/post/assets/hero.png`,
		// @ sanitizes to - in store paths (externalStorePath).
		`/a/imp-1/external/127.0.0.1/post/assets/hero-2x.png 2x`,
		`https://fonts.googleapis.com/css2?family=Inter`, // kept-cdn, untouched
		`src="data:image/gif;base64,R0lGODlh"`,           // skip-class untouched
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry missing %q:\n%s", want, entry)
		}
	}
	if strings.Contains(entry, `src="assets/`) {
		t.Errorf("unresolved relative ref survived:\n%s", entry)
	}

	byPath := map[string]ManifestRow{}
	for _, m := range res.Manifest {
		byPath[m.Path] = m
	}
	wantStatus := map[string]string{
		"external/127.0.0.1/post/assets/hero.png":        StatusBaked,
		"external/127.0.0.1/post/assets/hero-2x.png":     StatusBaked,
		"https://fonts.googleapis.com/css2?family=Inter": StatusKeptCDN,
		srv.URL + "/post/missing.png":                    StatusKeptExternal,
	}
	for path, want := range wantStatus {
		m, ok := byPath[path]
		if !ok {
			t.Errorf("manifest missing %q (have %v)", path, pathsOf(res))
			continue
		}
		if m.Status != want {
			t.Errorf("manifest %q status = %q, want %q", path, m.Status, want)
		}
	}

	// The fetch failure reason rides on the kept-external row (D5).
	if got := byPath[srv.URL+"/post/missing.png"].Reason; got != "status 404" {
		t.Errorf("kept-external reason = %q, want %q", got, "status 404")
	}
	// Baked assets carry the fetched bytes.
	if f := res.Files["external/127.0.0.1/post/assets/hero.png"]; string(f.Data) != "PNG1" {
		t.Errorf("baked bytes = %q", f.Data)
	}
}

// TestProcessBaseHrefOverrides: a <base href> in the document wins over the
// source URL as the resolution base.
func TestProcessBaseHrefOverrides(t *testing.T) {
	srv := baseTestServer(t)
	base := mustURL(t, srv.URL+"/post/")

	files := map[string][]byte{
		"index.html": []byte(`<!doctype html><html><head>` +
			`<base href="` + srv.URL + `/cdn/">` +
			`</head><body><img src="img.png"></body></html>`),
	}
	res, err := basePipeline().Process(context.Background(), files, "index.html", "imp-2", base)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(string(res.Entry), `/a/imp-2/external/127.0.0.1/cdn/img.png`) {
		t.Errorf("base href not honored:\n%s", res.Entry)
	}
	if f := res.Files["external/127.0.0.1/cdn/img.png"]; string(f.Data) != "CDNPNG" {
		t.Errorf("baked bytes = %q, want the /cdn/ asset", f.Data)
	}
}

// TestProcessRelativeOnKeepHost: a relative ref that resolves onto a
// keep-external allowlist host is kept external, never fetched.
func TestProcessRelativeOnKeepHost(t *testing.T) {
	// No server: any fetch would hit the live network and fail the test.
	base := mustURL(t, "https://fonts.googleapis.com/css2/")
	files := map[string][]byte{
		"index.html": []byte(`<html><head><link rel="stylesheet" href="family.css"></head></html>`),
	}
	res, err := basePipeline().Process(context.Background(), files, "index.html", "imp-3", base)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !strings.Contains(string(res.Entry), `href="https://fonts.googleapis.com/css2/family.css"`) {
		t.Errorf("kept ref not rewritten to the resolved URL:\n%s", res.Entry)
	}
	for _, m := range res.Manifest {
		if m.Status == StatusBaked {
			t.Errorf("unexpected baked row %q — allowlisted host must not be fetched", m.Path)
		}
	}
}

// TestProcessExternalCSSRelativeRefs: relative refs inside a baked external
// stylesheet resolve against the stylesheet's own URL (the gap fix).
func TestProcessExternalCSSRelativeRefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/css/main.css":
			_, _ = w.Write([]byte("@font-face{src:url(../fonts/a.woff2)}"))
		case "/fonts/a.woff2":
			_, _ = w.Write([]byte("WOFF"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Pack upload (nil base): the fix applies wherever external CSS is baked.
	files := map[string][]byte{
		"index.html": []byte(`<html><head><link rel="stylesheet" href="` + srv.URL + `/css/main.css"></head></html>`),
	}
	res, err := basePipeline().Process(context.Background(), files, "index.html", "cssfix-1", nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	css := string(res.Files["external/127.0.0.1/css/main.css"].Data)
	if !strings.Contains(css, "/a/cssfix-1/external/127.0.0.1/fonts/a.woff2") {
		t.Errorf("baked CSS not rewritten:\n%s", css)
	}
	if f := res.Files["external/127.0.0.1/fonts/a.woff2"]; string(f.Data) != "WOFF" {
		t.Errorf("font not baked from the CSS ref (have %v)", pathsOf(res))
	}
}

func pathsOf(res *Result) []string {
	var out []string
	for _, m := range res.Manifest {
		out = append(out, m.Path)
	}
	return out
}
