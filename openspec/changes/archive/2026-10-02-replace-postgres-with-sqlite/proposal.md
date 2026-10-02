## Why

The write-side database holds three small tables (pages, assets, counters) updated a handful of times per day by a single admin plane, yet costs a full networked Postgres server: a container in every dev run, a testcontainer in every integration build, and an operational backup story that doesn't exist (a lost host loses all bookkeeping while the bucket keeps serving). SQLite in WAL mode covers this workload with a single file, and Litestream replication to the same S3 bucket gives the first real recovery story — shrinking the bookkeeping loss window from unbounded to ~1s.

## What Changes

- **BREAKING** Replace Postgres/pgx/v5 with SQLite (`modernc.org/sqlite`, pure Go) for all write-side persistence: `db`, `slug`, `lifecycle`, boot in `cmd/server`, config.
- `DATABASE_URL` is replaced by `SQLITE_PATH` (required for admin/all, ignored for serve). Migration files are rewritten for SQLite dialect.
- Deployment gains a Litestream **sidecar** that ships the SQLite WAL to the same S3 bucket under the reserved `_db/` prefix, and a restore-if-absent step before admin boot. The app binary itself does not embed Litestream.
- **BREAKING** Deployment contract changes from "concurrent admin replicas are safe (advisory lock)" to **at most one admin writer**; failover = restore from the replica and start an admin elsewhere. Same-file concurrent boot stays safe via SQLite's write lock.
- The async replication window (bucket ahead of a restored DB by ≤1s) is accepted and documented as a visible compromise, alongside `kept-external`.
- Test infrastructure: the Postgres testcontainer disappears from the app and e2e suites; db/slug/migrate/lifecycle/upload tests move into the plain `go test ./...` suite against a temp file. The e2e harness keeps only MinIO. `cmd/pgmigrate` alone keeps pgx and a Postgres testcontainer for its one-time row-move test.
- One-time data migration path from an existing Postgres instance (pages, assets, counters — counters copied exactly).

## Capabilities

### New Capabilities

- `sqlite-persistence`: the SQLite-backed write-side store — schema, migrations, single-writer rule, WAL pragmas, and Litestream replication/restore contract (replica location, restore window).

### Modified Capabilities

- `deployment-modes`: required config becomes `SQLITE_PATH` (not `DATABASE_URL`) for admin/all; the "concurrent admin boots migrate safely" requirement is replaced by a single-writer requirement with same-file boot safety; boot duties gain restore-before-migrate; health ping wording no longer names Postgres.

## Impact

- Code: `internal/db` (driver + migrations), `internal/slug`, `internal/lifecycle`, `internal/upload` (unique-violation detection), `cmd/server/main.go`, `internal/config`, `internal/e2e/harness_test.go`, unit/integration tests for db, slug, lifecycle, upload, config.
- Dependencies: +`modernc.org/sqlite`; −`pgx/v5` and −testcontainers postgres module from the app (both retained solely for the `cmd/pgmigrate` row mover and its test).
- Infra/docs: `docker-compose.yml` (drop postgres), README config table, AGENTS.md invariants, deployment notes (Litestream sidecar config, restore procedure, edge purge unchanged).
- Serve plane and edge contract: unchanged.

## Non-goals

- No active-active or multi-writer admin; no HA beyond restore-based failover.
- No in-process Litestream library and no custom snapshot mechanism — the sidecar binary owns replication.
- No sub-second recovery-point engineering (sync-before-ack around lifecycle ops); the ≤1s window is accepted, not eliminated.
- No migration tooling beyond a one-time row move; no dual-write period.
- No change to the storage seam, ingest pipeline, serving path, or edge contract.
