//go:build integration

package s3compat

import (
	"context"
	"time"

	"testing"

	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/minio"

	"page/internal/config"
	"page/internal/storage/storagetest"
)

// ensureBucket retries bucket creation: MinIO's health endpoint can answer
// before the S3 API is fully initialized ("Server not initialized yet").
func ensureBucket(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var err error
	for i := 0; i < 20; i++ {
		if err = store.EnsureBucket(ctx); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("EnsureBucket: %v", err)
}

// Integration test: boots a real MinIO via testcontainers and runs the same
// conformance suite the mem driver passes (D7 driver-swap acceptance).
// Run with: go test -tags=integration ./...
func TestConformanceAgainstMinIO(t *testing.T) {
	ctx := context.Background()

	mc, err := minio.Run(ctx, "quay.io/minio/minio:latest",
		tc.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		}),
	)
	if err != nil {
		t.Fatalf("start minio container: %v", err)
	}
	t.Cleanup(func() { _ = mc.Terminate(ctx) })

	// ConnectionString returns "host:port"; credentials are the ones we set.
	endpoint, err := mc.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	store, err := New(config.Storage{
		Driver:    "s3compat",
		Endpoint:  endpoint,
		Bucket:    "pages",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ensureBucket(t, ctx, store)

	storagetest.Run(t, ctx, store)
}
