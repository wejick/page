// Package db holds write-side persistence: migrations and the queries used
// by the upload API. The serve path never touches this package (design D13).
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrateLockKey is the fixed advisory-lock key every replica uses
// ("pagemigr" packed into an int64). Holding it session-scoped on one
// connection serializes concurrent Migrate calls (multiple admin replicas
// booting at once).
const migrateLockKey int64 = 0x706167656D696772

// Migrate applies pending migrations in order, tracking them in
// schema_migrations. Safe to run on every boot: a session-scoped advisory
// lock serializes concurrent callers, and the loser re-checks each migration
// and skips the ones already applied.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
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

	// The advisory lock is session-scoped, so lock, version check, and apply
	// loop all run on one explicitly acquired connection.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("db: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("db: advisory lock: %w", err)
	}
	// Deferred unlock (runs before the Release above) covers failure paths.
	// WithoutCancel keeps the unlock working when ctx died mid-migration. If
	// the unlock fails, close the connection instead of pooling it: the
	// session ends and Postgres drops the lock with it.
	defer func() {
		var unlocked bool
		if err := conn.QueryRow(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock($1)`, migrateLockKey).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("db: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", name, err)
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO schema_migrations (version) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("db: apply migration %s: %w", name, err)
		}
	}
	return nil
}
