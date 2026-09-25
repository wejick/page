# Tasks: split-serve-admin-deployment

## 1. Configuration

- [x] 1.1 Add `Mode` to `internal/config` (`SERVER_MODE`: `serve`/`admin`/`all`, default `all`; invalid value fails) and per-mode validation: `serve` requires only storage settings; `admin`/`all` require `DATABASE_URL` + `AUTH_TOKEN` + storage. Unit tests: each mode's required/optional matrix, invalid mode rejection, default = `all`.

## 2. Plane-scoped router

- [x] 2.1 Add the mode to `serve.Options` and mount routes per plane in `serve.New`: serve → `/p/*`, `/a/*`, `/healthz` only; admin → `/`, `/api/*`, `/healthz` only; all → everything. Unit tests: table-driven requests per mode asserting mounted surfaces (serve `/api` → 404, admin `/p/` → 404, all unchanged).

## 3. Per-mode health

- [x] 3.1 Wire health probes per mode in `cmd/server`: serve mode passes a storage `Stat` probe (any response incl. `ErrNotFound` = healthy) as the `Ping` func; admin/all keep `pool.Ping`. Unit test: storage probe returns healthy on `ErrNotFound` and unhealthy on transport error (injectable store).

## 4. Mode-gated boot

- [x] 4.1 Gate boot duties in `cmd/server` on mode: open pgxpool, `db.Migrate`, `EnsureBucket`, lifecycle `Sweep`, and upload/lifecycle handler construction only in `admin`/`all`; serve mode boots straight from validated storage config. Unit-level compile safety plus an integration test that a serve-mode instance serves a seeded page end-to-end with no `DATABASE_URL` in its environment.

## 5. Migration advisory lock

- [x] 5.1 Wrap `db.Migrate`'s apply loop in a session-scoped `pg_advisory_lock` taken and released on one explicitly acquired pool connection (deferred unlock covers failures). Integration test: run two `Migrate` calls concurrently against the same fresh database — both succeed and the schema is fully applied.

## 6. End-to-end and docs

- [x] 6.1 Extend `internal/e2e`: alongside the existing `all`-mode flow, boot a second serve-mode instance over the same MinIO bucket and assert the plane contract (page + asset serve, `/api` and `/` → 404, health 200); assert the admin instance answers `/api/pages/{slug}` with lifecycle status.
- [x] 6.2 README: two-instance deployment topology (env matrix per instance, read-only credentials for the serve tier, admin network-restriction guidance), `SERVER_MODE` env table row; `make test` + `gofmt`/`go vet` clean.
