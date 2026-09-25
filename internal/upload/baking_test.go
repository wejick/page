//go:build integration

package upload

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// 6.5 acceptance: the wired pipeline bakes/keeps/records correctly and
// fetch failures never fail the upload.
func TestUploadBakingWiring(t *testing.T) {
	ctx := context.Background()
	h, store := newTestHandler(t, ctx, 0)

	html := `<!doctype html><html><head>` +
		`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Inter">` +
		`<script src="https://cdn.jsdelivr.net/npm/x.js"></script>` +
		`</head><body><img src="http://127.0.0.1:1/unreachable.png"></body></html>`
	body, ctype := multipartBody(t, "page.html", []byte(html),
		map[string]string{"identifier": "bake"})

	rec := post(h, body, ctype, "secret")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s, want 201", rec.Code, rec.Body)
	}
	for _, want := range []string{`"kept_cdn":2`, `"kept_external":1`, `"baked":0`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("missing %s in %s", want, rec.Body)
		}
	}

	// Stored entry: kept refs keep their (upgraded) URLs; the unreachable
	// one was upgraded http→https and nothing was fetched.
	obj, err := store.Get(ctx, "bake-1/index.html")
	if err != nil {
		t.Fatalf("entry missing: %v", err)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(obj.Reader)
	entry := buf.String()
	for _, want := range []string{
		"https://fonts.googleapis.com/css2?family=Inter",
		"https://cdn.jsdelivr.net/npm/x.js",
		"https://127.0.0.1:1/unreachable.png",
	} {
		if !strings.Contains(entry, want) {
			t.Fatalf("entry missing %q:\n%s", want, entry)
		}
	}
	if strings.Contains(entry, `"http://127.0.0.1:1`) {
		t.Fatalf("kept external not upgraded to https:\n%s", entry)
	}

	// Manifest records all three external rows with statuses.
	rec3 := getMeta(h, "bake-1")
	for _, want := range []string{
		`"path":"https://fonts.googleapis.com/css2?family=Inter"`,
		`"path":"https://cdn.jsdelivr.net/npm/x.js"`,
		`"status":"kept-cdn"`,
		`"path":"http://127.0.0.1:1/unreachable.png"`,
		`"status":"kept-external"`,
	} {
		if !bytes.Contains(rec3.Body.Bytes(), []byte(want)) {
			t.Fatalf("manifest missing %q in %s", want, rec3.Body)
		}
	}
}
