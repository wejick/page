//go:build integration

package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"mime/multipart"
	"path/filepath"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	miniomod "github.com/testcontainers/testcontainers-go/modules/minio"
	_ "modernc.org/sqlite"

	"page/internal/auth"
	"page/internal/config"
	"page/internal/db"
	"page/internal/storage/s3compat"
)

// ensureBucket retries bucket creation: MinIO's health endpoint can answer
// before the S3 API is fully initialized ("Server not initialized yet").
func ensureBucket(t *testing.T, ctx context.Context, store *s3compat.Store) {
	t.Helper()
	var err error
	for i := 0; i < 20; i++ {
		if err = store.EnsureBucket(ctx); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("bucket: %v", err)
}

// dbPing is the admin-plane health probe over a SQLite handle: a real query
// round trip through the schema, exactly what cmd/server wires.
func dbPing(d *sql.DB) func(context.Context) error {
	return func(ctx context.Context) error { return db.Ping(ctx, d) }
}

// startDB opens a migrated SQLite file in a per-test temp dir — the admin
// plane's database needs no container anymore.
func startDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

// startMinio boots a real MinIO testcontainer (S3-compatible driver,
// path-style) and returns a store over the "pages" bucket — created with
// retries once the S3 API is up — plus the raw endpoint for storage env maps.
func startMinio(t *testing.T, ctx context.Context) (*s3compat.Store, string) {
	t.Helper()
	mc, err := miniomod.Run(ctx, "quay.io/minio/minio:latest",
		tc.WithEnv(map[string]string{
			"MINIO_ROOT_USER": "minioadmin", "MINIO_ROOT_PASSWORD": "minioadmin",
		}))
	if err != nil {
		t.Fatalf("minio: %v", err)
	}
	t.Cleanup(func() { _ = mc.Terminate(ctx) })
	endpoint, err := mc.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("minio endpoint: %v", err)
	}
	store, err := s3compat.New(config.Storage{
		Driver: "s3compat", Endpoint: endpoint, Bucket: "pages",
		AccessKey: "minioadmin", SecretKey: "minioadmin", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3compat: %v", err)
	}
	ensureBucket(t, ctx, store)
	return store, endpoint
}

// seedPack is the minimal page pack: entry HTML referencing a stylesheet and
// an asset by relative path.
var seedPack = map[string]string{
	"index.html": `<!doctype html><html><head>
<link rel="stylesheet" href="style.css">
</head><body>
<img src="assets/hero.png">
</body></html>`,
	"style.css":       "body{background:url(assets/bg.png)}",
	"assets/hero.png": "HERO",
	"assets/bg.png":   "BG",
}

// uploadBody zips the pack and wraps it in the multipart form the upload API
// expects, returning the body and its content type.
func uploadBody(t *testing.T, pack map[string]string, identifier string) (*bytes.Buffer, string) {
	t.Helper()
	zipBuf := &bytes.Buffer{}
	zw := zip.NewWriter(zipBuf)
	for name, content := range pack {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mb := &bytes.Buffer{}
	mw := multipart.NewWriter(mb)
	fw, _ := mw.CreateFormFile("file", "pack.zip")
	_, _ = fw.Write(zipBuf.Bytes())
	_ = mw.WriteField("identifier", identifier)
	_ = mw.Close()
	return mb, mw.FormDataContentType()
}

// tokenChecker builds the static-bearer checker the classic journeys
// authenticate with (auth-modes D1: the default mode is unchanged).
func tokenChecker() *auth.Checker {
	return auth.NewChecker(config.AuthModeToken, "secret", nil)
}
