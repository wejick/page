package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
func CreatePage(ctx context.Context, pool *pgxpool.Pool, rec PageRecord, assets []AssetRow, totalBytes int64) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO pages (slug, identifier, code, asset_count, total_bytes)
			 VALUES ($1, $2, $3, $4, $5)`,
			rec.Slug, rec.Identifier, rec.Code, len(assets), totalBytes,
		); err != nil {
			return err
		}
		for _, a := range assets {
			if _, err := tx.Exec(ctx,
				`INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				rec.Slug, a.Path, a.SourceURL, a.ContentType, a.Bytes, a.Status,
			); err != nil {
				return err
			}
		}
		return nil
	})
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

// GetPage returns the page metadata and manifest, or ErrNotFound.
func GetPage(ctx context.Context, pool *pgxpool.Pool, slug string) (*PageMeta, []AssetView, error) {
	meta := &PageMeta{}
	err := pool.QueryRow(ctx, `
		SELECT slug, identifier, code, asset_count, total_bytes, created_at, status
		FROM pages WHERE slug = $1`, slug,
	).Scan(&meta.Slug, &meta.Identifier, &meta.Code, &meta.AssetCount, &meta.TotalBytes, &meta.CreatedAt, &meta.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("db: get page: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT path, source_url, content_type, bytes, status
		FROM assets WHERE slug = $1 ORDER BY path`, slug)
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

// ErrNotFound marks a missing page.
var ErrNotFound = errors.New("db: page not found")

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
func ListPages(ctx context.Context, pool *pgxpool.Pool, status string, limit, offset int) ([]*PageMeta, int, error) {
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
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pages WHERE ($1::text IS NULL OR status = $1)`, st,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count pages: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT slug, identifier, code, asset_count, total_bytes, created_at, status
		FROM pages WHERE ($1::text IS NULL OR status = $1)
		ORDER BY created_at DESC, slug DESC
		LIMIT $2 OFFSET $3`, st, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: list pages: %w", err)
	}
	defer rows.Close()

	pages := []*PageMeta{}
	for rows.Next() {
		var m PageMeta
		if err := rows.Scan(&m.Slug, &m.Identifier, &m.Code, &m.AssetCount,
			&m.TotalBytes, &m.CreatedAt, &m.Status); err != nil {
			return nil, 0, fmt.Errorf("db: scan page: %w", err)
		}
		pages = append(pages, &m)
	}
	return pages, total, rows.Err()
}

// DeletePage removes the page row; its assets cascade. Returns ErrNotFound
// when no row matches (the delete API maps its guarded transition to 404
// earlier, so this is a backstop against a concurrent race).
func DeletePage(ctx context.Context, pool *pgxpool.Pool, slug string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM pages WHERE slug = $1`, slug)
	if err != nil {
		return fmt.Errorf("db: delete page: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
