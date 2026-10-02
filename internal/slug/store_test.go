package slug

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"page/internal/db"
)

// openDB opens a fresh migrated SQLite file in a per-test temp dir.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func TestAllocateSequential(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)

	for i, want := range []int{1, 2, 3} {
		code, err := Allocate(ctx, d, "landing-page")
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		if code != want {
			t.Fatalf("code = %d, want %d", code, want)
		}
	}
	// Independent counters per identifier.
	if code, _ := Allocate(ctx, d, "pricing"); code != 1 {
		t.Fatalf("pricing first code = %d, want 1", code)
	}
}

func TestAllocateConcurrent(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)

	const n = 25
	var mu sync.Mutex
	seen := make(map[int]bool, n)
	var wg sync.WaitGroup
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := Allocate(ctx, d, "concurrent-test")
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[code] {
				t.Errorf("concurrent allocate: duplicate code %d", code)
			}
			seen[code] = true
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent allocate: %v", err)
		}
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct codes, want %d", len(seen), n)
	}
}

// The unique-violation detection the upload retry loop depends on: a dup
// (identifier, code) insert surfaces as a recognizable SQLite constraint
// error through database/sql wrapping.
func TestIsUniqueViolation(t *testing.T) {
	ctx := context.Background()
	d := openDB(t)

	if _, err := d.ExecContext(ctx,
		`INSERT INTO pages (slug, identifier, code) VALUES ('x-1', 'x', 1)`); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	_, err := d.ExecContext(ctx,
		`INSERT INTO pages (slug, identifier, code) VALUES ('x-1-bis', 'x', 1)`)
	if err == nil {
		t.Fatal("duplicate (identifier, code) insert succeeded, want violation")
	}
	if !IsUniqueViolation(err) {
		t.Fatalf("IsUniqueViolation(%v) = false, want true", err)
	}
	if IsUniqueViolation(context.Canceled) {
		t.Fatal("IsUniqueViolation(canceled) = true, want false")
	}
}
