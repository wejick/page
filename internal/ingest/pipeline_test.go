package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const goldenSlug = "golden-1"

func TestPipelineRewrite(t *testing.T) {
	bakerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BAKED-PNG-BYTES"))
	}))
	defer bakerSrv.Close()

	pack := map[string][]byte{
		"index.html": []byte(`<!doctype html><html><head>` +
			`<link rel="stylesheet" href="style.css">` +
			`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter">` +
			`<script src="https://cdn.jsdelivr.net/npm/app.js"></script>` +
			`</head><body>` +
			`<img src="assets/hero.png" srcset="assets/hero.png 1x, assets/hero@2x.png 2x">` +
			`<img src="` + bakerSrv.URL + `/pic.png">` +
			`<img src="https://fonts.gstatic.com/signed.woff2?Expires=1&amp;Signature=x">` +
			`<img src="missing/local.png">` +
			`<div style="background-image:url('assets/bg.png')"></div>` +
			`<style>body{background:url(assets/pattern.png)}</style>` +
			`</body></html>`),
		"style.css":           []byte("@import \"theme.css\";\nbody{background:url(assets/from-css.png)}"),
		"theme.css":           []byte(".t{color:red}"),
		"assets/hero.png":     []byte("PNG1"),
		"assets/hero@2x.png":  []byte("PNG2"),
		"assets/bg.png":       []byte("BG"),
		"assets/pattern.png":  []byte("PAT"),
		"assets/from-css.png": []byte("FROMCSS"),
	}

	pipeline := &Pipeline{
		Keep: KeepRules{
			Fonts: []string{"fonts.googleapis.com"},
			JS:    []string{"cdn.jsdelivr.net"},
		},
		Fetch: NewFetcher(1<<20, 500*time.Millisecond, 3*time.Second, 4),
	}
	res, err := pipeline.Process(context.Background(), pack, "index.html", goldenSlug)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Entry HTML matches the golden (host placeholder substituted).
	entry := strings.ReplaceAll(string(res.Entry), extractHost(bakerSrv.URL), "{{BAKER}}")
	compareGolden(t, "testdata/golden_index.html", entry)
	compareGolden(t, "testdata/golden_style.css", string(res.Files["style.css"].Data))

	// Rewritten stylesheet import chain is stored too.
	if got := string(res.Files["theme.css"].Data); got != ".t{color:red}" {
		t.Fatalf("theme.css = %q", got)
	}

	// Manifest statuses cover the full taxonomy.
	byPath := map[string]ManifestRow{}
	for _, m := range res.Manifest {
		byPath[m.Path] = m
	}
	wantStatus := map[string]string{
		"style.css":                  StatusLocal,
		"theme.css":                  StatusLocal,
		"assets/hero.png":            StatusLocal,
		"assets/hero@2x.png":         StatusLocal,
		"assets/bg.png":              StatusLocal,
		"assets/pattern.png":         StatusLocal,
		"assets/from-css.png":        StatusLocal,
		"external/127.0.0.1/pic.png": StatusBaked,
		"https://fonts.googleapis.com/css2?family=Inter":               StatusKeptCDN,
		"https://cdn.jsdelivr.net/npm/app.js":                          StatusKeptCDN,
		"https://fonts.gstatic.com/signed.woff2?Expires=1&Signature=x": StatusKeptExternal,
	}
	for path, want := range wantStatus {
		m, ok := byPath[path]
		if !ok {
			t.Errorf("manifest missing %q", path)
			continue
		}
		if m.Status != want {
			t.Errorf("manifest %q status = %q, want %q", path, m.Status, want)
		}
	}
	if len(res.Manifest) != len(wantStatus) {
		t.Errorf("manifest has %d rows, want %d", len(res.Manifest), len(wantStatus))
	}

	// The baked asset carries the fetched bytes.
	if f := res.Files["external/127.0.0.1/pic.png"]; string(f.Data) != "BAKED-PNG-BYTES" {
		t.Fatalf("baked bytes = %q", f.Data)
	}
}

func extractHost(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}
	return u.Host
}

func compareGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("GOLDEN_UPDATE") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with GOLDEN_UPDATE=1 to create): %v", err)
	}
	if string(want) != got {
		t.Errorf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
