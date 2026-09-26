//go:build integration

package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedPages inserts pages with distinct created_at ordering: slug "p-<i>"
// gets the i-th oldest timestamp, so "newest first" means descending i.
// A status of "" maps to the default (live).
func seedPages(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statuses map[string]string) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		slug := fmt.Sprintf("p-%d", i)
		ident, code := "p", i+1
		status, ok := statuses[slug]
		if !ok || status == "" {
			status = "live"
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO pages (slug, identifier, code, status, created_at)
			VALUES ($1, $2, $3, $4, $5)`,
			slug, ident, code, status, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}
}

func TestListPages(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedPages(t, ctx, pool, map[string]string{"p-0": "parked", "p-1": "parked"})

	// Ordering: newest first.
	pages, total, err := ListPages(ctx, pool, "", 50, 0)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	wantOrder := []string{"p-4", "p-3", "p-2", "p-1", "p-0"}
	for i, want := range wantOrder {
		if pages[i].Slug != want {
			t.Fatalf("pages[%d].slug = %q, want %q", i, pages[i].Slug, want)
		}
	}
	if pages[0].Status != "live" || pages[4].Status != "parked" {
		t.Fatalf("statuses = %q/%q, want live/parked at ends", pages[0].Status, pages[4].Status)
	}

	// Window: limit/offset slices the same ordering, total stays unfiltered.
	pages, total, err = ListPages(ctx, pool, "", 2, 2)
	if err != nil {
		t.Fatalf("ListPages window: %v", err)
	}
	if total != 5 {
		t.Fatalf("window total = %d, want 5", total)
	}
	if len(pages) != 2 || pages[0].Slug != "p-2" || pages[1].Slug != "p-1" {
		t.Fatalf("window = %v/%v, want p-2/p-1", pages[0].Slug, pages[1].Slug)
	}

	// Last page: offset past the remaining rows yields an empty slice, not nil.
	pages, _, err = ListPages(ctx, pool, "", 2, 4)
	if err != nil {
		t.Fatalf("ListPages tail: %v", err)
	}
	if len(pages) != 1 || pages[0].Slug != "p-0" {
		t.Fatalf("tail = %v, want [p-0]", pages)
	}

	// Status filter: total counts only matching rows.
	pages, total, err = ListPages(ctx, pool, "parked", 50, 0)
	if err != nil {
		t.Fatalf("ListPages filter: %v", err)
	}
	if total != 2 || len(pages) != 2 {
		t.Fatalf("filter total/len = %d/%d, want 2/2", total, len(pages))
	}
	for _, p := range pages {
		if p.Status != "parked" {
			t.Fatalf("filter returned status %q, want parked", p.Status)
		}
	}

	// Filter with no matches: empty result, zero total.
	pages, total, err = ListPages(ctx, pool, "deleting", 50, 0)
	if err != nil || total != 0 || len(pages) != 0 {
		t.Fatalf("empty filter = %v/%d/%v, want 0/0/nil", pages, total, err)
	}

	// Clamps: absurd limit clamps to the cap (still all 5 rows here), and a
	// zero limit takes the default without error.
	if _, total, err = ListPages(ctx, pool, "", 100000, 0); err != nil || total != 5 {
		t.Fatalf("clamped limit = %d/%v, want 5/nil", total, err)
	}
	if _, _, err = ListPages(ctx, pool, "", 0, 0); err != nil {
		t.Fatalf("default limit: %v", err)
	}
}

func TestDeletePage(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t, ctx)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedPages(t, ctx, pool, nil)
	if _, err := pool.Exec(ctx, `
		INSERT INTO assets (slug, path, source_url, content_type, bytes, status)
		VALUES ('p-2', 'style.css', 'src', 'text/css', 10, 'local')`); err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	if err := DeletePage(ctx, pool, "p-2"); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	var pages, assets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pages`).Scan(&pages); err != nil || pages != 4 {
		t.Fatalf("pages after delete = %d/%v, want 4", pages, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM assets`).Scan(&assets); err != nil || assets != 0 {
		t.Fatalf("assets after cascade = %d/%v, want 0", assets, err)
	}

	// Unknown slug is distinguishable.
	if err := DeletePage(ctx, pool, "p-2"); err == nil {
		t.Fatal("deleting a missing row succeeded, want ErrNotFound")
	}
}
