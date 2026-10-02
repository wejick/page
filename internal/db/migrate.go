// Package db holds write-side persistence: migrations and the queries used
// by the upload API. The serve path never touches this package (design D13).
// The store is a single SQLite file in WAL mode, opened with per-connection
// pragmas via the DSN; at most one admin writer exists per file
// (replace-postgres-with-sqlite D3).
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isBusy reports whether err is SQLite's lock-contention code, which the
// journal-mode conversion can return instantly — without consulting the
// busy handler — when another connection holds the write lock on a database
// that is still being created.
func isBusy(err error) bool {
	var sqErr *sqlite.Error
	return errors.As(err, &sqErr) && sqErr.Code() == sqlite3.SQLITE_BUSY
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens the SQLite database at path with the pragmas every connection
// needs (replace-postgres-with-sqlite D1): a write-lock wait budget (concurrent
// boots and the Litestream sidecar contend here), WAL journaling, FK
// enforcement (off by default in SQLite — assets cascade relies on it), and
// NORMAL fsync (durable on process crash; power-cut durability is the
// sidecar's replica). Pragmas are per-connection, so they ride the DSN and
// apply to every connection the pool hands out. The wait budget must use the
// `_busy_timeout` shorthand, not `_pragma=busy_timeout(...)`: the driver
// applies the shorthand first, then the _pragma list lexicographically —
// and journal_mode takes a brief lock, which dead-spins to SQLITE_BUSY when
// no wait budget is set yet.
// SQLite creates files but not directories, so a fresh deployment pointing
// at data/page.db needs the parent made here or boot dies with
// SQLITE_CANTOPEN.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("db: create directory %s: %w", dir, err)
		}
	}
	dsn := "file:" + path +
		"?_busy_timeout=5000" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	return db, nil
}

// Migrate applies pending migrations in order, tracking them in
// schema_migrations. Safe to run on every boot. The whole run sits in one
// BEGIN IMMEDIATE transaction (replace-postgres-with-sqlite D4): SQLite's
// write lock serializes concurrent callers — a second admin booting against
// the same file blocks on busy_timeout, then re-reads schema_migrations and
// skips everything applied — which replaces the Postgres advisory lock.
func Migrate(ctx context.Context, db *sql.DB) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("db: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// Version checks, DDL, and the commit all run on one pinned connection.
	// Acquisition carries a bounded retry: when two admins boot against a
	// fresh file simultaneously, the loser can be opening its first
	// connection while the winner holds the write lock mid-conversion — an
	// instant SQLITE_BUSY that no busy_timeout absorbs.
	var conn *sql.Conn
	var cerr error
	for attempt := 0; ; attempt++ {
		conn, cerr = db.Conn(ctx)
		if cerr == nil || !isBusy(cerr) || attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("db: acquire connection: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if cerr != nil {
		return fmt.Errorf("db: acquire connection: %w", cerr)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("db: begin migrate: %w", err)
	}
	committed := false
	// Rollback on any failure path, including a canceled context.
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER))
		)`); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	for _, name := range names {
		var applied int
		if err := conn.QueryRowContext(ctx,
			`SELECT count(*) FROM schema_migrations WHERE version = ?1`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("db: check migration %s: %w", name, err)
		}
		if applied == 1 {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", name, err)
		}
		if _, err := conn.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("db: apply migration %s: %w", name, err)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?1)`, name); err != nil {
			return fmt.Errorf("db: record migration %s: %w", name, err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("db: commit migrate: %w", err)
	}
	committed = true
	return nil
}
