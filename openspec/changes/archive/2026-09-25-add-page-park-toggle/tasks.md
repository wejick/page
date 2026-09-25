# Tasks: add-page-park-toggle

## 1. Storage seam: prefix `Copy`

- [x] 1.1 Add `Copy(srcPrefix, dstPrefix string) error` to `storage.Storage`; implement in `mem` driver (map iteration, preserves bytes/content-type/ETag, overwrites existing destination keys, no-op on empty prefix). Unit tests in `internal/storage/mem` covering preservation, source-unchanged, overwrite, and empty-prefix cases.
- [x] 1.2 Implement `Copy` in `s3compat` via internal `ListObjectsV2` + `CopyObject` (bounded concurrency, non-multipart since objects are ≤10 MiB). Add `Copy` conformance cases to `storagetest` (bytes/content-type/ETag preserved, overwrite semantics) and an integration test against real MinIO via testcontainers asserting ETag stability across copy.

## 2. Lifecycle state in Postgres

- [x] 2.1 Add migration `0002_park_status.sql`: `ALTER TABLE pages ADD COLUMN status TEXT NOT NULL DEFAULT 'live' CHECK (status IN ('live','parking','parked','unparking'))`. Integration test: fresh database has the column with default `live`; upgrading the `0001` schema preserves existing rows as `live`; CHECK rejects invalid values.

## 3. Lifecycle engine

- [x] 3.1 Create `internal/lifecycle`: `Park(ctx, slug)` / `Unpark(ctx, slug)` implementing D2 copy-before-delete sweeps (`Copy` then `DeletePrefix`) with guarded status transitions (`UPDATE … WHERE status = <expected>` → `parking`/`unparking` before mutating objects, finalize after). Integration tests (real Postgres + MinIO): park→serve 404s, unpark→serve 200 with identical bytes and stable ETag, unknown slug error, re-invoking a mid-state toggle converges idempotently.
- [x] 3.2 Add boot sweep: after `db.Migrate` in `cmd/server`, resume any rows in `parking`/`unparking` to completion. Integration test: seed a split state (status `parking`, objects in both prefixes), run sweep, assert convergence to `parked` with no data loss (and the mirror case for `unparking`).

## 4. Admin API surface

- [x] 4.1 Add routes `POST /api/pages/{slug}/park` and `POST /api/pages/{slug}/unpark` with the existing bearer-token auth; semantics: `200` + `{"status": …}`, `401` unauthenticated, `404` unknown slug, idempotent on same-state. Unit tests with `httptest` covering all four outcomes.
- [x] 4.2 Add `status` to the `GET /api/pages/{slug}` manifest response. Unit test asserting the field appears and reflects the current state (`live` after upload, `parked` after park).

## 5. Serving changes

- [x] 5.1 Split cache headers in `internal/serve`: entry HTML gets `Cache-Control: public, max-age=60, must-revalidate` + ETag; assets keep `public, max-age=31536000, immutable` + ETag. Update `serve` unit tests and `internal/e2e` header assertions for both entry and asset responses.
- [x] 5.2 Add TTL-bounded revalidation to the HTML cache: new `HTML_CACHE_TTL` env (default 60s) in `internal/config`; entries revalidate via `Stat` after the TTL, dropping the entry and serving `404` on miss. Unit tests with a counting storage wrapper: no roundtrip within TTL, one `Stat` after TTL, parked page → `404` and eviction, unknown slugs still not negatively cached.

## 6. End-to-end and docs

- [x] 6.1 Extend `internal/e2e` integration suite: upload → serve → park → 404 within TTL → unpark → `200` byte-identical with pre-park ETag (`304` on conditional request); concurrent toggle safety smoke (parallel park/unpark converge to a consistent state).
- [x] 6.2 Update README (routes, `HTML_CACHE_TTL` env row, `_parked/` prefix note, CDN-purge ops step for parked assets) and run `make test` + `gofmt`/`go vet` clean.
