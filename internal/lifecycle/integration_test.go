//go:build integration

package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tc "github.com/testcontainers/testcontainers-go"
	miniomod "github.com/testcontainers/testcontainers-go/modules/minio"
	postgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"page/internal/config"
	"page/internal/db"
	"page/internal/storage"
	"page/internal/storage/s3compat"
)

// Integration tests: real Postgres + MinIO via testcontainers.
// Run with: go test -tags=integration ./internal/lifecycle/

type harness struct {
	pool  *pgxpool.Pool
	store storage.Storage
	svc   *Service
	api   *API
}

func start(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	pgc, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("page"), postgres.WithUsername("page"),
		postgres.WithPassword("page"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })
	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mc, err := miniomod.Run(ctx, "quay.io/minio/minio:latest",
		tc.WithEnv(map[string]string{
			"MINIO_ROOT_USER": "minioadmin", "MINIO_ROOT_PASSWORD": "minioadmin",
		}))
	if err != nil {
		t.Fatalf("minio: %v", err)
	}
	t.Cleanup(func() { _ = mc.Terminate(ctx) })
	endpoint, err := mc.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("minio endpoint: %v", err)
	}
	store, err := s3compat.New(config.Storage{
		Driver: "s3compat", Endpoint: endpoint, Bucket: "pages",
		AccessKey: "minioadmin", SecretKey: "minioadmin", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3compat: %v", err)
	}
	ensureBucket(t, ctx, store)

	svc := New(pool, store)
	return &harness{pool: pool, store: store, svc: svc, api: NewAPI(svc, "secret")}
}

// ensureBucket retries bucket creation: MinIO's health endpoint can answer
// before the S3 API is fully initialized ("Server not initialized yet").
func ensureBucket(t *testing.T, ctx context.Context, store *s3compat.Store) {
	t.Helper()
	var err error
	for i := 0; i < 20; i++ {
		if err = store.EnsureBucket(ctx); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("bucket: %v", err)
}

// seedPage inserts a page row (status live) and two objects.
func (h *harness) seedPage(t *testing.T, ctx context.Context, slug string) {
	t.Helper()
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO pages (slug, identifier, code) VALUES ($1, $2, 1)`, slug, slug); err != nil {
		t.Fatalf("seed page row: %v", err)
	}
	if err := h.store.Put(ctx, slug+"/index.html", "text/html; charset=utf-8",
		strings.NewReader("<h1>"+slug+"</h1>")); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	if err := h.store.Put(ctx, slug+"/assets/hero.png", "image/png",
		strings.NewReader("HERO")); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
}

func (h *harness) mustExist(t *testing.T, ctx context.Context, key string, want bool) {
	t.Helper()
	_, err := h.store.Stat(ctx, key)
	if (err == nil) != want {
		t.Fatalf("key %s exists = %v (err=%v), want %v", key, err == nil, err, want)
	}
}

func (h *harness) mustStatus(t *testing.T, ctx context.Context, slug, want string) {
	t.Helper()
	var status string
	if err := h.pool.QueryRow(ctx,
		`SELECT status FROM pages WHERE slug = $1`, slug).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
}

func TestParkUnparkRoundtrip(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "rt-1")

	// Entry ETag before parking; it must survive the roundtrip.
	meta, err := h.store.Stat(ctx, "rt-1/index.html")
	if err != nil {
		t.Fatalf("stat entry: %v", err)
	}

	// Park: 200 + parked, objects moved, live prefix gone.
	got, err := h.svc.Park(ctx, "rt-1")
	if err != nil || got != StatusParked {
		t.Fatalf("Park = %q, %v; want parked", got, err)
	}
	h.mustStatus(t, ctx, "rt-1", StatusParked)
	h.mustExist(t, ctx, "rt-1/index.html", false)
	h.mustExist(t, ctx, "rt-1/assets/hero.png", false)
	h.mustExist(t, ctx, "_parked/rt-1/index.html", true)
	h.mustExist(t, ctx, "_parked/rt-1/assets/hero.png", true)

	// Park again: idempotent.
	if got, err := h.svc.Park(ctx, "rt-1"); err != nil || got != StatusParked {
		t.Fatalf("re-Park = %q, %v; want parked", got, err)
	}

	// Unpark: objects restored byte-identically under the original keys with
	// the pre-park ETag (stable across park/unpark).
	if got, err := h.svc.Unpark(ctx, "rt-1"); err != nil || got != StatusLive {
		t.Fatalf("Unpark = %q, %v; want live", got, err)
	}
	h.mustStatus(t, ctx, "rt-1", StatusLive)
	h.mustExist(t, ctx, "_parked/rt-1/index.html", false)

	after, err := h.store.Get(ctx, "rt-1/index.html")
	if err != nil {
		t.Fatalf("get restored entry: %v", err)
	}
	if after.ETag != meta.ETag {
		t.Fatalf("etag after unpark = %q, want pre-park %q", after.ETag, meta.ETag)
	}
	buf := make([]byte, after.Size)
	if _, err := after.Reader.Read(buf); err != nil && err.Error() != "EOF" {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "<h1>rt-1</h1>" {
		t.Fatalf("restored bytes = %q", buf)
	}

	// Unpark again: idempotent.
	if got, err := h.svc.Unpark(ctx, "rt-1"); err != nil || got != StatusLive {
		t.Fatalf("re-Unpark = %q, %v; want live", got, err)
	}
}

func TestParkUnknownSlug(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	if _, err := h.svc.Park(ctx, "ghost-9"); err != ErrNotFound {
		t.Fatalf("Park unknown = %v, want ErrNotFound", err)
	}
	if _, err := h.svc.Unpark(ctx, "ghost-9"); err != ErrNotFound {
		t.Fatalf("Unpark unknown = %v, want ErrNotFound", err)
	}
}

func TestOppositeToggleIsBusy(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "busy-1")

	// Simulate a concurrent unpark holding the transition state.
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'unparking' WHERE slug = 'busy-1'`); err != nil {
		t.Fatalf("force status: %v", err)
	}
	if _, err := h.svc.Park(ctx, "busy-1"); err != ErrBusy {
		t.Fatalf("Park during unpark = %v, want ErrBusy", err)
	}
}

func TestResumeInterruptedPark(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "crash-1")

	// Simulate a crash mid-park: intent recorded, objects split — the parked
	// copy exists but the live prefix was never deleted.
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'parking' WHERE slug = 'crash-1'`); err != nil {
		t.Fatalf("force status: %v", err)
	}
	if err := h.store.Copy(ctx, "crash-1/", "_parked/crash-1/"); err != nil {
		t.Fatalf("partial copy: %v", err)
	}

	// Re-invoking the same toggle resumes and converges.
	if got, err := h.svc.Park(ctx, "crash-1"); err != nil || got != StatusParked {
		t.Fatalf("resume Park = %q, %v; want parked", got, err)
	}
	h.mustExist(t, ctx, "crash-1/index.html", false)
	h.mustExist(t, ctx, "_parked/crash-1/index.html", true)
	h.mustStatus(t, ctx, "crash-1", StatusParked)
}

func TestSweepResumesInterruptedToggles(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "sw-park-1")
	h.seedPage(t, ctx, "sw-unpark-1")

	// Park sw-unpark-1 fully, then force both pages into mid-flight states.
	if _, err := h.svc.Park(ctx, "sw-unpark-1"); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'parking' WHERE slug = 'sw-park-1'`); err != nil {
		t.Fatalf("force parking: %v", err)
	}
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'unparking' WHERE slug = 'sw-unpark-1'`); err != nil {
		t.Fatalf("force unparking: %v", err)
	}

	// Boot sweep converges both.
	if err := h.svc.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	h.mustStatus(t, ctx, "sw-park-1", StatusParked)
	h.mustExist(t, ctx, "sw-park-1/index.html", false)
	h.mustExist(t, ctx, "_parked/sw-park-1/index.html", true)

	h.mustStatus(t, ctx, "sw-unpark-1", StatusLive)
	h.mustExist(t, ctx, "sw-unpark-1/index.html", true)
	h.mustExist(t, ctx, "_parked/sw-unpark-1/index.html", false)
}

func TestAPIOutcomes(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "api-1")

	ts := httptest.NewServer(func() http.Handler {
		mux := http.NewServeMux()
		mux.Handle("POST /api/pages/{slug}/park", h.api.Park())
		mux.Handle("POST /api/pages/{slug}/unpark", h.api.Unpark())
		mux.Handle("DELETE /api/pages/{slug}", h.api.Delete())
		return mux
	}())
	t.Cleanup(ts.Close)

	post := func(path, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	del := func(path, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("DELETE", ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE %s: %v", path, err)
		}
		return resp
	}

	// Authenticated park succeeds.
	resp := post("/api/pages/api-1/park", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("park status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	h.mustStatus(t, ctx, "api-1", StatusParked)

	// Idempotent park: 200 again.
	resp = post("/api/pages/api-1/park", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-park status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown slug: 404.
	resp = post("/api/pages/ghost-9/park", "secret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown slug status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Unpark restores.
	resp = post("/api/pages/api-1/unpark", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unpark status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	h.mustStatus(t, ctx, "api-1", StatusLive)

	// Delete during a lifecycle transition: 409, page untouched.
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'parking' WHERE slug = 'api-1'`); err != nil {
		t.Fatalf("force parking: %v", err)
	}
	resp = del("/api/pages/api-1", "secret")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete during transition status = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	h.mustStatus(t, ctx, "api-1", StatusParking)
	h.mustExist(t, ctx, "api-1/index.html", true)

	// Settle the page, then delete via API: 200, objects and row gone.
	if _, err := h.svc.Park(ctx, "api-1"); err != nil {
		t.Fatalf("park: %v", err)
	}
	resp = del("/api/pages/api-1", "secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	h.mustExist(t, ctx, "_parked/api-1/index.html", false)
	var rows int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM pages WHERE slug = 'api-1'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows after delete = %d/%v, want 0", rows, err)
	}

	// Deleting it again: 404.
	resp = del("/api/pages/api-1", "secret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("re-delete status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// 401 is covered by the unit test (auth rejects before any I/O).
}

func TestDeleteLivePage(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "del-1")
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO counters (identifier, next) VALUES ('del-1', 2)`); err != nil {
		t.Fatalf("seed counter: %v", err)
	}

	if err := h.svc.Delete(ctx, "del-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	h.mustExist(t, ctx, "del-1/index.html", false)
	h.mustExist(t, ctx, "del-1/assets/hero.png", false)
	h.mustExist(t, ctx, "_parked/del-1/index.html", false)

	var rows int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM pages WHERE slug = 'del-1'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("page rows after delete = %d/%v, want 0", rows, err)
	}
	var assets int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM assets WHERE slug = 'del-1'`).Scan(&assets); err != nil || assets != 0 {
		t.Fatalf("asset rows after delete = %d/%v, want 0", assets, err)
	}
	// Delete is slug-neutral like park: counters are untouched.
	var next int
	if err := h.pool.QueryRow(ctx,
		`SELECT next FROM counters WHERE identifier = 'del-1'`).Scan(&next); err != nil || next != 2 {
		t.Fatalf("counter after delete = %d/%v, want 2", next, err)
	}

	// Delete again: the row is gone, so ErrNotFound (no idempotent re-delete
	// of a slug that no longer exists).
	if err := h.svc.Delete(ctx, "del-1"); err != ErrNotFound {
		t.Fatalf("re-Delete = %v, want ErrNotFound", err)
	}
}

func TestDeleteParkedPage(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "delp-1")
	if _, err := h.svc.Park(ctx, "delp-1"); err != nil {
		t.Fatalf("park: %v", err)
	}

	if err := h.svc.Delete(ctx, "delp-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	h.mustExist(t, ctx, "_parked/delp-1/index.html", false)
	h.mustExist(t, ctx, "_parked/delp-1/assets/hero.png", false)
	h.mustExist(t, ctx, "delp-1/index.html", false)
	h.mustExist(t, ctx, "delp-1/assets/hero.png", false)

	var rows int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM pages WHERE slug = 'delp-1'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("page rows after delete = %d/%v, want 0", rows, err)
	}
}

func TestDeleteUnknownSlugAndBusy(t *testing.T) {
	ctx := context.Background()
	h := start(t)

	if err := h.svc.Delete(ctx, "ghost-9"); err != ErrNotFound {
		t.Fatalf("Delete unknown = %v, want ErrNotFound", err)
	}

	// Simulate an in-flight park: delete must refuse and leave everything.
	h.seedPage(t, ctx, "busy-2")
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'parking' WHERE slug = 'busy-2'`); err != nil {
		t.Fatalf("force status: %v", err)
	}
	if err := h.svc.Delete(ctx, "busy-2"); err != ErrBusy {
		t.Fatalf("Delete during park = %v, want ErrBusy", err)
	}
	h.mustStatus(t, ctx, "busy-2", StatusParking)
	h.mustExist(t, ctx, "busy-2/index.html", true)
}

func TestDeleteResumesMidFlight(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "crash-del-1")

	// Simulate a crash mid-delete: intent recorded, objects still in place.
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'deleting' WHERE slug = 'crash-del-1'`); err != nil {
		t.Fatalf("force status: %v", err)
	}

	// Re-invoking the same Delete resumes and converges.
	if err := h.svc.Delete(ctx, "crash-del-1"); err != nil {
		t.Fatalf("resume Delete: %v", err)
	}
	h.mustExist(t, ctx, "crash-del-1/index.html", false)
	var rows int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM pages WHERE slug = 'crash-del-1'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("page rows after resume = %d/%v, want 0", rows, err)
	}
}

func TestSweepResumesInterruptedDelete(t *testing.T) {
	ctx := context.Background()
	h := start(t)
	h.seedPage(t, ctx, "sw-del-1")

	// Crash mid-delete: status recorded, objects not yet touched.
	if _, err := h.pool.Exec(ctx,
		`UPDATE pages SET status = 'deleting' WHERE slug = 'sw-del-1'`); err != nil {
		t.Fatalf("force deleting: %v", err)
	}

	if err := h.svc.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	h.mustExist(t, ctx, "sw-del-1/index.html", false)
	h.mustExist(t, ctx, "_parked/sw-del-1/index.html", false)
	var rows int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM pages WHERE slug = 'sw-del-1'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("page rows after sweep = %d/%v, want 0", rows, err)
	}

	// The page no longer appears in the list (same filter the API serves).
	pages, total, err := db.ListPages(ctx, h.pool, "", 50, 0)
	if err != nil || total != 0 || len(pages) != 0 {
		t.Fatalf("list after sweep = %d/%d/%v, want 0/0/nil", len(pages), total, err)
	}
}
