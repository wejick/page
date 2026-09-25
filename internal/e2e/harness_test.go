//go:build integration

package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tc "github.com/testcontainers/testcontainers-go"
	miniomod "github.com/testcontainers/testcontainers-go/modules/minio"
	postgres "github.com/testcontainers/testcontainers-go/modules/postgres"

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

// testPostgres bundles the running container with its migrated pool so a
// test can retire either mid-test (the serve-mode boot proof does exactly
// that); everything else just lets the cleanups run.
type testPostgres struct {
	Container *postgres.PostgresContainer
	Pool      *pgxpool.Pool
}

// startPostgres boots a real Postgres testcontainer, opens a pool over it,
// and applies the migrations. Pool close and container terminate are
// registered as cleanups here.
func startPostgres(t *testing.T, ctx context.Context) *testPostgres {
	t.Helper()
	pgc, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("page"), postgres.WithUsername("page"),
		postgres.WithPassword("page"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("postgres: %v", err)
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
	return &testPostgres{Container: pgc, Pool: pool}
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
