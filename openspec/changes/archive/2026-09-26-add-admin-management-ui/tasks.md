# Tasks: add-admin-management-ui

## 1. Schema: `deleting` status

- [x] 1.1 Add migration `0003_delete_status.sql`: drop the `pages_status_check` CHECK and re-add it extended with `deleting` (D2). Integration test: fresh database accepts `deleting`; upgrading the `0002` schema preserves existing rows and their statuses; invalid values still rejected.

## 2. DB queries

- [x] 2.1 Add `db.ListPages(ctx, pool, status string, limit, offset int)` returning rows plus total count (single `COUNT(*)` with the same filter, `ORDER BY created_at DESC, slug DESC`, default 50 / cap 500 clamped in code per D4). Integration test: ordering, `limit`/`offset` windows, `status` filter, total with filter, empty result.
- [x] 2.2 Add `db.DeletePage(ctx, pool, slug)` deleting the `pages` row (assets cascade). Integration test: row and assets removed; unknown slug is a no-op or returns a distinguishable outcome — pick one, documented at the call site.

## 3. Delete engine

- [x] 3.1 Add `lifecycle.Service.Delete(ctx, slug)`: per-slug lock, guarded transition `live|parked → deleting` (`0 rows` ⇒ inspect status: unknown slug `404`, transient `parking`/`unparking` `ErrBusy` → `409`), then idempotent `DeletePrefix` on both `{slug}/` and `_parked/{slug}/`, then `db.DeletePage` (D1, D3). Integration tests (real Postgres + MinIO): live page deleted (objects + rows gone, counters untouched), parked page deleted (`_parked/` cleared), unknown slug, delete-during-park conflict leaves both intact.
- [x] 3.2 Extend `Sweep` to resume `deleting` rows: re-run both prefix deletions and remove the row (idempotent, mirrors the toggle resume path). Integration test: seed `deleting` with objects in either prefix and/or row present, run sweep, assert objects gone, row gone, page absent from list.

## 4. API surface

- [x] 4.1 Add `upload.Handler.List` for `GET /api/pages` with bearer auth; response `{"total": n, "pages": [...]}` per the page-management spec (D4). Unit tests with `httptest`: totals and ordering, `limit`/`offset`, `status` filter, clamp above 500, `401` unauthenticated, empty list.
- [x] 4.2 Add `lifecycle.API.Delete` for `DELETE /api/pages/{slug}` with bearer auth; `200` on success, `401`, `404`, `409` with the existing conflict message (D1). Unit tests with `httptest` covering all four outcomes.
- [x] 4.3 Mount both routes in `internal/serve`'s `api()` block so they exist on admin/all and 404 on serve (D5). Unit test: serve-mode mux 404s `GET /api/pages` and `DELETE /api/pages/{slug}`; admin/all mux routes them.

## 5. Management UI

- [x] 5.1 Rewrite `internal/serve/static/index.html` as the hash-routed management UI (D6): list view (table, status badges, prev/next pagination off `total`, status filter), detail view (metadata + manifest table with per-asset statuses), upload view preserving the current form behavior; token entry persisted in `localStorage`; `confirm()` before delete; transient statuses shown with actions disabled; 401 prompts for the token. Handler test: `GET /{$}` still serves the file on admin/all.
- [x] 5.2 Exercise the UI against a running dev server (`make up && make run`): upload → appears in list → park → badge flips → delete → gone; list 401 flow prompts for token. Fix what the exercise surfaces.

## 6. End-to-end and docs

- [x] 6.1 Extend `internal/e2e`: upload → list shows page → park → delete → `404` at `/p/{slug}/` within revalidation window → row absent from list; delete-during-toggle `409` smoke. Run `go test -tags=integration ./...` green.
- [x] 6.2 Update README (API routes, delete semantics + `_parked/` cleanup note, CDN-purge ops step after delete) and AGENTS.md (module layout notes); `gofmt -l .` empty, `go vet ./...` and `go vet -tags=integration ./...` clean, `make test` green.
