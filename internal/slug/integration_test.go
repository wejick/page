//go:build integration

package slug

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	postgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"page/internal/db"
)

func mustDSN(t *testing.T, ctx context.Context, pgc *postgres.PostgresContainer) string {
	t.Helper()
	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

func startPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pgc, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("page"),
		postgres.WithUsername("page"),
		postgres.WithPassword("page"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })
	pool, err := pgxpool.New(ctx, mustDSN(t, ctx, pgc))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func TestAllocateSequential(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)

	for i, want := range []int{1, 2, 3} {
		code, err := Allocate(ctx, pool, "landing-page")
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		if code != want {
			t.Fatalf("code = %d, want %d", code, want)
		}
	}
	// Independent counters per identifier.
	if code, _ := Allocate(ctx, pool, "pricing"); code != 1 {
		t.Fatalf("pricing first code = %d, want 1", code)
	}
}

func TestAllocateConcurrent(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)

	const n = 25
	var mu sync.Mutex
	seen := make(map[int]bool, n)
	var wg sync.WaitGroup
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := Allocate(ctx, pool, "concurrent-test")
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[code] {
				errCh <- context.Canceled // duplicate code
				return
			}
			seen[code] = true
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent allocate: %v", err)
		}
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct codes, want %d", len(seen), n)
	}
}
