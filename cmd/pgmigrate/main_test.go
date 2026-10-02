//go:build integration

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jackc/pgx/v5"
	postgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"page/internal/db"
	"page/internal/slug"
)

// Integration test: a real Postgres holds the old deployment's rows;
// Move copies them into a fresh SQLite file unchanged.
// Run with: go test -tags=integration ./cmd/pgmigrate/
func TestMovePreservesRowsAndCounters(t *testing.T) {
	ctx := context.Background()

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

	// Seed the source exactly as the Postgres-era schema left it.
	src, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect source: %v", err)
	}
	defer src.Close(ctx)
	if _, err := src.Exec(ctx, `
		CREATE TABLE pages (
			slug        TEXT PRIMARY KEY,
			identifier  TEXT NOT NULL,
			code        INT  NOT NULL,
			asset_count INT  NOT NULL DEFAULT 0,
			total_bytes BIGINT NOT NULL DEFAULT 0,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			status      TEXT NOT NULL DEFAULT 'live'
		);
		CREATE TABLE assets (
			slug TEXT NOT NULL, path TEXT NOT NULL, source_url TEXT NOT NULL,
			content_type TEXT NOT NULL, bytes BIGINT NOT NULL, status TEXT NOT NULL,
			PRIMARY KEY (slug, path)
		);
		CREATE TABLE counters (identifier TEXT PRIMARY KEY, next INT NOT NULL DEFAULT 2);`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{sql: `INSERT INTO pages (slug, identifier, code, asset_count, total_bytes, created_at, status)
		  VALUES ('demo-1', 'demo', 1, 2, 150, $1, 'parked')`, args: []any{createdAt}},
		{sql: `INSERT INTO pages (slug, identifier, code, asset_count, total_bytes, created_at, status)
		  VALUES ('demo-2', 'demo', 2, 0, 40, $1, 'live')`, args: []any{createdAt.Add(time.Minute)}},
		{sql: `INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		  VALUES ('demo-1', 'index.html', '', 'text/html; charset=utf-8', 120, 'local')`},
		{sql: `INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		  VALUES ('demo-1', 'app.js', 'https://cdn.example/app.js', 'text/javascript', 30, 'kept-cdn')`},
		{sql: `INSERT INTO counters (identifier, next) VALUES ('demo', 3)`},
		{sql: `INSERT INTO counters (identifier, next) VALUES ('other', 2)`},
	} {
		if _, err := src.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed rows: %v", err)
		}
	}

	// Move into a fresh migrated SQLite file.
	targetPath := filepath.Join(t.TempDir(), "page.db")
	target, err := db.Open(targetPath)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	if err := db.Migrate(ctx, target); err != nil {
		t.Fatalf("migrate target: %v", err)
	}
	if err := Move(ctx, src, target); err != nil {
		t.Fatalf("Move: %v", err)
	}

	// Pages: rows and timestamps land unchanged (millis preserve ordering).
	var parkedCreated, liveCreated int64
	var status string
	if err := target.QueryRowContext(ctx,
		`SELECT created_at, status FROM pages WHERE slug = 'demo-1'`).Scan(&parkedCreated, &status); err != nil {
		t.Fatalf("select demo-1: %v", err)
	}
	if status != "parked" || parkedCreated != createdAt.UnixMilli() {
		t.Fatalf("demo-1 = %q@%d, want parked@%d", status, parkedCreated, createdAt.UnixMilli())
	}
	if err := target.QueryRowContext(ctx,
		`SELECT created_at FROM pages WHERE slug = 'demo-2'`).Scan(&liveCreated); err != nil {
		t.Fatalf("select demo-2: %v", err)
	}
	if liveCreated != createdAt.Add(time.Minute).UnixMilli() {
		t.Fatalf("demo-2 created_at = %d, want %d", liveCreated, createdAt.Add(time.Minute).UnixMilli())
	}

	// Assets land with their manifest statuses.
	var assets int
	if err := target.QueryRowContext(ctx,
		`SELECT count(*) FROM assets WHERE slug = 'demo-1'`).Scan(&assets); err != nil || assets != 2 {
		t.Fatalf("demo-1 assets = %d/%v, want 2", assets, err)
	}

	// Counters are copied exactly: demo's next code is 3, and the next
	// allocation must return it (codes never move backward).
	var next int
	if err := target.QueryRowContext(ctx,
		`SELECT next FROM counters WHERE identifier = 'demo'`).Scan(&next); err != nil || next != 3 {
		t.Fatalf("demo counter = %d/%v, want 3", next, err)
	}
	code, err := slug.Allocate(ctx, target, "demo")
	if err != nil || code != 3 {
		t.Fatalf("allocate demo after move = %d/%v, want 3", code, err)
	}

	// A non-empty target is refused.
	if err := Move(ctx, src, target); err == nil {
		t.Fatal("Move into a non-empty target succeeded, want refusal")
	}
}
