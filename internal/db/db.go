package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AssetRow is one manifest row to persist.
type AssetRow struct {
	Path        string
	SourceURL   string
	ContentType string
	Bytes       int64
	Status      string
}

// PageRecord identifies a created page.
type PageRecord struct {
	Slug       string
	Identifier string
	Code       int
}

// CreatePage persists the page row and its manifest in one transaction.
func CreatePage(ctx context.Context, db *sql.DB, rec PageRecord, assets []AssetRow, totalBytes int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin create page: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pages (slug, identifier, code, asset_count, total_bytes)
		 VALUES (?1, ?2, ?3, ?4, ?5)`,
		rec.Slug, rec.Identifier, rec.Code, len(assets), totalBytes,
	); err != nil {
		return fmt.Errorf("db: insert page: %w", err)
	}
	for _, a := range assets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
			 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
			rec.Slug, a.Path, a.SourceURL, a.ContentType, a.Bytes, a.Status,
		); err != nil {
			return fmt.Errorf("db: insert asset %s: %w", a.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit create page: %w", err)
	}
	return nil
}

// PageMeta is the API view of a page.
type PageMeta struct {
	Slug       string    `json:"slug"`
	Identifier string    `json:"identifier"`
	Code       int       `json:"code"`
	AssetCount int       `json:"asset_count"`
	TotalBytes int64     `json:"total_bytes"`
	CreatedAt  time.Time `json:"created_at"`
	Status     string    `json:"status"` // lifecycle: live/parking/parked/unparking
}

// AssetView is the API view of one manifest row.
type AssetView struct {
	Path        string `json:"path"`
	SourceURL   string `json:"source_url"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	Status      string `json:"status"`
}

// ErrNotFound marks a missing page.
var ErrNotFound = errors.New("db: page not found")

// Ping is the admin/all health probe: a real query round trip through the
// schema (deployment-modes). database/sql's Ping only proves a connection
// opens — a corrupt-but-openable file would report healthy — so the probe
// reads a row instead.
func Ping(ctx context.Context, db *sql.DB) error {
	var one int
	if err := db.QueryRowContext(ctx,
		`SELECT 1 FROM schema_migrations LIMIT 1`).Scan(&one); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	return nil
}

// timestampMillis scans created_at as unix milliseconds
// (replace-postgres-with-sqlite D5): millisecond granularity preserves the
// sub-second created_at ordering the schema had with TIMESTAMPTZ.
func millisTime(ms int64) time.Time { return time.UnixMilli(ms) }

// GetPage returns the page metadata and manifest, or ErrNotFound.
func GetPage(ctx context.Context, db *sql.DB, slug string) (*PageMeta, []AssetView, error) {
	meta := &PageMeta{}
	var createdAt int64
	err := db.QueryRowContext(ctx, `
		SELECT slug, identifier, code, asset_count, total_bytes, created_at, status
		FROM pages WHERE slug = ?1`, slug,
	).Scan(&meta.Slug, &meta.Identifier, &meta.Code, &meta.AssetCount, &meta.TotalBytes, &createdAt, &meta.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("db: get page: %w", err)
	}
	meta.CreatedAt = millisTime(createdAt)

	rows, err := db.QueryContext(ctx, `
		SELECT path, source_url, content_type, bytes, status
		FROM assets WHERE slug = ?1 ORDER BY path`, slug)
	if err != nil {
		return nil, nil, fmt.Errorf("db: list assets: %w", err)
	}
	defer rows.Close()

	assets := []AssetView{}
	for rows.Next() {
		var a AssetView
		if err := rows.Scan(&a.Path, &a.SourceURL, &a.ContentType, &a.Bytes, &a.Status); err != nil {
			return nil, nil, fmt.Errorf("db: scan asset: %w", err)
		}
		assets = append(assets, a)
	}
	return meta, assets, rows.Err()
}

// List window clamps (add-admin-management-ui D4): hardcoded because no
// scenario needs operator tuning — a config knob would be speculative.
const (
	listDefaultLimit = 50
	listMaxLimit     = 500
)

// ListPages returns page metadata ordered newest first, optionally filtered
// by lifecycle status ("" = all), plus the total number of pages matching the
// filter so a paginated UI can render controls. limit <= 0 takes the default
// (50); values above the cap (500) are clamped; negative offsets become 0.
// One query shape serves both filter cases: ?1 binds a status or NULL, and
// `?1 IS NULL` short-circuits the predicate.
func ListPages(ctx context.Context, db *sql.DB, status string, limit, offset int) ([]*PageMeta, int, error) {
	if limit <= 0 {
		limit = listDefaultLimit
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	var st any // nil = no filter; one query shape serves both statements
	if status != "" {
		st = status
	}

	var total int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pages WHERE (?1 IS NULL OR status = ?1)`, st,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count pages: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT slug, identifier, code, asset_count, total_bytes, created_at, status
		FROM pages WHERE (?1 IS NULL OR status = ?1)
		ORDER BY created_at DESC, slug DESC
		LIMIT ?2 OFFSET ?3`, st, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: list pages: %w", err)
	}
	defer rows.Close()

	pages := []*PageMeta{}
	for rows.Next() {
		var m PageMeta
		var createdAt int64
		if err := rows.Scan(&m.Slug, &m.Identifier, &m.Code, &m.AssetCount,
			&m.TotalBytes, &createdAt, &m.Status); err != nil {
			return nil, 0, fmt.Errorf("db: scan page: %w", err)
		}
		m.CreatedAt = millisTime(createdAt)
		pages = append(pages, &m)
	}
	return pages, total, rows.Err()
}

// DeletePage removes the page row; its assets cascade. Returns ErrNotFound
// when no row matches (the delete API maps its guarded transition to 404
// earlier, so this is a backstop against a concurrent race).
func DeletePage(ctx context.Context, db *sql.DB, slug string) error {
	tag, err := db.ExecContext(ctx, `DELETE FROM pages WHERE slug = ?1`, slug)
	if err != nil {
		return fmt.Errorf("db: delete page: %w", err)
	}
	if n, err := tag.RowsAffected(); err != nil {
		return fmt.Errorf("db: delete page rows: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}
