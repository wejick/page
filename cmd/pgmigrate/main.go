// Command pgmigrate performs the one-time move from an existing Postgres
// deployment to SQLite (replace-postgres-with-sqlite D7): pages, assets, and
// counters are copied row by row — counters exactly, since slug codes never
// move backward. The target must be a fresh (rowless) migrated SQLite file;
// the command refuses to write into a database that already holds pages.
//
// Usage: pgmigrate -pg $DATABASE_URL -sqlite /data/page.db
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"page/internal/db"
)

// Stats reports what moved.
type Stats struct {
	Pages    int
	Assets   int
	Counters int
}

func main() {
	pgDSN := flag.String("pg", os.Getenv("DATABASE_URL"), "source Postgres connection string")
	sqlitePath := flag.String("sqlite", os.Getenv("SQLITE_PATH"), "target SQLite database file")
	if err := run(context.Background(), *pgDSN, *sqlitePath); err != nil {
		fmt.Fprintln(os.Stderr, "pgmigrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, pgDSN, sqlitePath string) error {
	if pgDSN == "" {
		return fmt.Errorf("source required: -pg or DATABASE_URL")
	}
	if sqlitePath == "" {
		return fmt.Errorf("target required: -sqlite or SQLITE_PATH")
	}
	target, err := db.Open(sqlitePath)
	if err != nil {
		return err
	}
	defer target.Close()
	if err := db.Migrate(ctx, target); err != nil {
		return fmt.Errorf("migrate target: %w", err)
	}

	conn, err := pgx.Connect(ctx, pgDSN)
	if err != nil {
		return fmt.Errorf("connect source: %w", err)
	}
	defer conn.Close(ctx)
	if err := conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping source: %w", err)
	}

	return Move(ctx, conn, target)
}

// Move copies every row from the Postgres source into the migrated SQLite
// target in one target transaction. It refuses a target that already holds
// pages — re-running against a partially-migrated store would duplicate.
func Move(ctx context.Context, src *pgx.Conn, target *sql.DB) error {
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin target tx: %w", err)
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pages`).Scan(&existing); err != nil {
		return fmt.Errorf("check target: %w", err)
	}
	if existing != 0 {
		return fmt.Errorf("target already holds %d pages; refusing to move into a non-empty database", existing)
	}

	rows, err := src.Query(ctx, `
		SELECT slug, identifier, code, asset_count, total_bytes, created_at, status
		FROM pages ORDER BY slug`)
	if err != nil {
		return fmt.Errorf("read source pages: %w", err)
	}
	defer rows.Close()

	insertPage, err := tx.PrepareContext(ctx, `
		INSERT INTO pages (slug, identifier, code, asset_count, total_bytes, created_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare page insert: %w", err)
	}
	defer insertPage.Close()

	var stats Stats
	for rows.Next() {
		var slug, identifier, status string
		var code, assetCount int
		var totalBytes int64
		var createdAt time.Time
		if err := rows.Scan(&slug, &identifier, &code, &assetCount, &totalBytes, &createdAt, &status); err != nil {
			return fmt.Errorf("scan page %s: %w", slug, err)
		}
		if _, err := insertPage.ExecContext(ctx,
			slug, identifier, code, assetCount, totalBytes, createdAt.UnixMilli(), status); err != nil {
			return fmt.Errorf("insert page %s: %w", slug, err)
		}
		stats.Pages++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source pages: %w", err)
	}

	assetRows, err := src.Query(ctx, `
		SELECT slug, path, source_url, content_type, bytes, status
		FROM assets ORDER BY slug, path`)
	if err != nil {
		return fmt.Errorf("read source assets: %w", err)
	}
	defer assetRows.Close()

	insertAsset, err := tx.PrepareContext(ctx, `
		INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare asset insert: %w", err)
	}
	defer insertAsset.Close()

	for assetRows.Next() {
		var slug, path, sourceURL, contentType, status string
		var nbytes int64
		if err := assetRows.Scan(&slug, &path, &sourceURL, &contentType, &nbytes, &status); err != nil {
			return fmt.Errorf("scan asset %s/%s: %w", slug, path, err)
		}
		if _, err := insertAsset.ExecContext(ctx,
			slug, path, sourceURL, contentType, nbytes, status); err != nil {
			return fmt.Errorf("insert asset %s/%s: %w", slug, path, err)
		}
		stats.Assets++
	}
	if err := assetRows.Err(); err != nil {
		return fmt.Errorf("iterate source assets: %w", err)
	}

	counterRows, err := src.Query(ctx, `SELECT identifier, next FROM counters ORDER BY identifier`)
	if err != nil {
		return fmt.Errorf("read source counters: %w", err)
	}
	defer counterRows.Close()

	insertCounter, err := tx.PrepareContext(ctx,
		`INSERT INTO counters (identifier, next) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare counter insert: %w", err)
	}
	defer insertCounter.Close()

	for counterRows.Next() {
		var identifier string
		var next int
		if err := counterRows.Scan(&identifier, &next); err != nil {
			return fmt.Errorf("scan counter %s: %w", identifier, err)
		}
		// Copied exactly: the next code this identifier will hand out must
		// not move (deleted slugs' codes are never reused, D6).
		if _, err := insertCounter.ExecContext(ctx, identifier, next); err != nil {
			return fmt.Errorf("insert counter %s: %w", identifier, err)
		}
		stats.Counters++
	}
	if err := counterRows.Err(); err != nil {
		return fmt.Errorf("iterate source counters: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit target tx: %w", err)
	}
	fmt.Printf("pgmigrate: moved %d pages, %d assets, %d counters\n",
		stats.Pages, stats.Assets, stats.Counters)
	return nil
}
