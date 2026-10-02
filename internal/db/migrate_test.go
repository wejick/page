package db

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// The schema applies on a fresh file and is idempotent; the constraints the
// manifest relies on hold (unique slug, unique (identifier, code), asset
// status CHECK, assets cascade on page delete).
func TestMigrateAppliesSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "page.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Idempotent.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}

	// pages: duplicate slug must fail (PK).
	if _, err := db.ExecContext(ctx, `INSERT INTO pages (slug, identifier, code) VALUES ('x-1','x',1)`); err != nil {
		t.Fatalf("insert page: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pages (slug, identifier, code) VALUES ('x-1','x',1)`); err == nil {
		t.Fatal("duplicate slug insert succeeded, want unique violation")
	}

	// pages: duplicate (identifier, code) must fail (unique index).
	if _, err := db.ExecContext(ctx, `INSERT INTO pages (slug, identifier, code) VALUES ('x-1-bis','x',1)`); err == nil {
		t.Fatal("duplicate (identifier, code) insert succeeded, want unique violation")
	}

	// counters: exists and allocatable.
	if _, err := db.ExecContext(ctx, `INSERT INTO counters (identifier, next) VALUES ('x', 2)`); err != nil {
		t.Fatalf("insert counter: %v", err)
	}
	var next int
	if err := db.QueryRowContext(ctx, `SELECT next FROM counters WHERE identifier = 'x'`).Scan(&next); err != nil {
		t.Fatalf("select counter: %v", err)
	}
	if next != 2 {
		t.Fatalf("next = %d, want 2", next)
	}

	// assets: status CHECK constraint holds.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		 VALUES ('x-1', 'index.html', 'src', 'text/html', 5, 'bogus')`); err == nil {
		t.Fatal("bogus asset status accepted, want CHECK violation")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		 VALUES ('x-1', 'index.html', 'src', 'text/html', 5, 'local')`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}

	// assets cascade on page delete (foreign_keys pragma is on by DSN).
	if _, err := db.ExecContext(ctx, `DELETE FROM pages WHERE slug = 'x-1'`); err != nil {
		t.Fatalf("delete page: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM assets`).Scan(&n); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if n != 0 {
		t.Fatalf("assets after cascade = %d, want 0", n)
	}

	// pages: the lifecycle CHECK holds the full vocabulary and nothing else.
	for _, ok := range []string{"live", "parking", "parked", "unparking", "deleting"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO pages (slug, identifier, code, status) VALUES (?1, ?2, 1, ?1)`, ok, ok); err != nil {
			t.Fatalf("status %q rejected: %v", ok, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pages (slug, identifier, code, status) VALUES ('v-1', 'v', 1, 'vanished')`); err == nil {
		t.Fatal("bogus page status accepted, want CHECK violation")
	}
}

// A fresh deployment points SQLITE_PATH at a file whose directory does not
// exist yet (README/Makefile: data/page.db); Open must create the parent.
func TestOpenCreatesParentDirectory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "deep", "page.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

// Two admin instances booting against the same file — separate database
// handles, as separate processes would hold — both migrate successfully
// exactly once (the write lock is per-connection, so the second handle
// exercises the real cross-handle contention).
func TestMigrateConcurrentHandles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "page.db")

	const handles = 2
	dbs := make([]*sql.DB, handles)
	for i := range dbs {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		t.Cleanup(func() { _ = d.Close() })
		dbs[i] = d
	}

	start := make(chan struct{})
	errs := make([]error, handles)
	var wg sync.WaitGroup
	for i := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, dbs[i])
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate on handle %d: %v", i, err)
		}
	}
	assertAllMigrationsApplied(t, ctx, dbs[0])
}

// Two admin processes booting against the same file at the same time: both
// Migrate calls must succeed and the schema must end up fully applied exactly
// once, with SQLite's write lock serializing the two (BEGIN IMMEDIATE,
// replace-postgres-with-sqlite D4).
func TestMigrateConcurrentBoots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "page.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Barrier so both calls contend for the write lock simultaneously.
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, db)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate %d: %v", i, err)
		}
	}
	assertAllMigrationsApplied(t, ctx, db)
}

// assertAllMigrationsApplied checks that every embedded migration is recorded
// in schema_migrations and, on a fresh database, that there are no others.
func assertAllMigrationsApplied(t *testing.T, ctx context.Context, db *sql.DB) {
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
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
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
