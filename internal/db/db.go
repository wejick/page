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
