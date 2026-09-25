//go:build integration

package db

import (
	"context"
	"io/fs"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	postgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Integration test: boots a real Postgres via testcontainers.
// Run with: go test -tags=integration ./...
func TestMigrateAppliesSchema(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}

	// pages: duplicate slug must fail (PK).
	if _, err := pool.Exec(ctx, `INSERT INTO pages (slug, identifier, code) VALUES ('x-1','x',1)`); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pages (slug, identifier, code) VALUES ('x-1','x',1)`); err == nil {
		t.Fatal("duplicate slug insert succeeded, want unique violation")
	}

	// pages: duplicate (identifier, code) must fail (unique index).
	if _, err := pool.Exec(ctx, `INSERT INTO pages (slug, identifier, code) VALUES ('x-1-bis','x',1)`); err == nil {
		t.Fatal("duplicate (identifier, code) insert succeeded, want unique violation")
	}

	// counters: exists and allocatable.
	if _, err := pool.Exec(ctx, `INSERT INTO counters (identifier, next) VALUES ('x', 2)`); err != nil {
		t.Fatalf("insert counter: %v", err)
	}
	var next int
	if err := pool.QueryRow(ctx, `SELECT next FROM counters WHERE identifier = 'x'`).Scan(&next); err != nil {
		t.Fatalf("select counter: %v", err)
	}
	if next != 2 {
		t.Fatalf("next = %d, want 2", next)
	}

	// assets: status CHECK constraint holds.
	if _, err := pool.Exec(ctx,
		`INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		 VALUES ('x-1', 'index.html', 'src', 'text/html', 5, 'bogus')`); err == nil {
		t.Fatal("bogus asset status accepted, want CHECK violation")
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		 VALUES ('x-1', 'index.html', 'src', 'text/html', 5, 'local')`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}

	// assets cascade on page delete.
	if _, err := pool.Exec(ctx, `DELETE FROM pages WHERE slug = 'x-1'`); err != nil {
		t.Fatalf("delete page: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM assets`).Scan(&n); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if n != 0 {
		t.Fatalf("assets after cascade = %d, want 0", n)
	}
}

// Two admin replicas booting at the same time: both Migrate calls must
// succeed and the schema must end up fully applied exactly once, with the
// advisory lock serializing the two sessions.
func TestMigrateConcurrentBoots(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)

	// Barrier so both calls contend for the advisory lock simultaneously.
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, pool)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate %d: %v", i, err)
		}
	}
	assertAllMigrationsApplied(t, ctx, pool)
}

// assertAllMigrationsApplied checks that every embedded migration is recorded
// in schema_migrations and, on a fresh database, that there are no others.
func assertAllMigrationsApplied(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	want := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			want = append(want, e.Name())
		}
	}

	got := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		t.Fatalf("select schema_migrations: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		got[v] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	for _, name := range want {
		if !got[name] {
			t.Errorf("migration %s not recorded in schema_migrations", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("schema_migrations has %d entries, want %d", len(got), len(want))
	}
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
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The 0001→0002 upgrade path: a database already at 0001 (with existing page
// rows) must come up as live — the toggle column is additive with a default.
func TestMigrateUpgradeFrom0001DefaultsLive(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)

	// Apply only 0001 by hand and register it as applied, exactly as a
	// pre-toggle deployment would look.
	sqlBytes, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read 0001: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ('0001_init.sql')`); err != nil {
		t.Fatalf("register 0001: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO pages (slug, identifier, code) VALUES ('old-1','old',1)`); err != nil {
		t.Fatalf("insert pre-toggle page: %v", err)
	}

	// Migrate must apply only 0002 and default the existing row to live.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate upgrade: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM pages WHERE slug = 'old-1'`).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "live" {
		t.Fatalf("upgraded row status = %q, want live", status)
	}

	// New rows default to live too.
	if _, err := pool.Exec(ctx,
		`INSERT INTO pages (slug, identifier, code) VALUES ('new-2','new',1)`); err != nil {
		t.Fatalf("insert post-toggle page: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM pages WHERE slug = 'new-2'`).Scan(&status); err != nil {
		t.Fatalf("select new status: %v", err)
	}
	if status != "live" {
		t.Fatalf("default status = %q, want live", status)
	}

	// The CHECK constraint rejects values outside the lifecycle vocabulary.
	if _, err := pool.Exec(ctx,
		`UPDATE pages SET status = 'vanished' WHERE slug = 'new-2'`); err == nil {
		t.Fatal("bogus page status accepted, want CHECK violation")
	}
	for _, ok := range []string{"live", "parking", "parked", "unparking"} {
		if _, err := pool.Exec(ctx,
			`UPDATE pages SET status = $1 WHERE slug = 'new-2'`, ok); err != nil {
			t.Fatalf("status %q rejected: %v", ok, err)
		}
	}
}
