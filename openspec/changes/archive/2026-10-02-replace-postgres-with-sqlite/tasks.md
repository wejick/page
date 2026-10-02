## 1. Dependencies and config

- [x] 1.1 Add `modernc.org/sqlite`; drop `jackc/pgx/v5` and the testcontainers postgres module from the app and test suites (both stay solely for `cmd/pgmigrate` and its Postgres-source test, per 3.3); `go build ./...` clean
- [x] 1.2 Rewrite `internal/config`: `SQLITE_PATH` (required for admin/all, ignored for serve) replaces `DATABASE_URL`; update `config_test.go` expectation tables; acceptance: `go test ./internal/config/` green

## 2. Database core

- [x] 2.1 Rewrite `internal/db/migrate.go`: open/hold a `*sql.DB`-based handle, per-connection pragmas via DSN (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(1)`, `synchronous(NORMAL)`), `Migrate` in one `BEGIN IMMEDIATE` transaction with the same `schema_migrations` version loop; migrate the three SQL files to SQLite dialect (`?`/`?NNN` placeholders, `INTEGER` millis `created_at`, CHECK constraints as-is); acceptance: unit test boots two concurrent `Migrate` calls against one temp file — both succeed, schema applied exactly once
- [x] 2.2 Rewrite `internal/db/db.go` queries (`CreatePage`, `GetPage`, `ListPages`, `DeletePage`) to `database/sql` with `?NNN` placeholders keeping the one-query-shape trick in `ListPages`; move `db_test.go` from `-tags=integration` into the plain suite against a temp file; acceptance: `go test ./internal/db/` green without Docker
- [x] 2.3 Rewrite `internal/slug` `Allocate`/`New` (`ON CONFLICT … RETURNING` → SQLite upsert-returning), `IsUniqueViolation` → `sqlite.Error` constraint-code check; move `slug/integration_test.go` into the plain suite; acceptance: `go test ./internal/slug/` green without Docker
- [x] 2.4 Rewrite `internal/lifecycle` pool call sites (`ANY($3)` → `IN (?,?,?)`, `pgx.ErrNoRows` → `sql.ErrNoRows`); move `lifecycle/integration_test.go` semantics into plain-suite tests where they don't need MinIO, keep the rest in the MinIO-only integration suite; acceptance: `go test ./internal/lifecycle/` green

## 3. Callers and boot

- [x] 3.1 Update `internal/upload` handler wiring (`*sql.DB` instead of `*pgxpool.Pool`) and its retry-on-unique-violation loop; update `upload_test.go`; acceptance: `go test ./internal/upload/` green
- [x] 3.2 Rewrite `cmd/server/main.go` admin/all boot: open SQLite (DSN pragmas), `db.Migrate`, health ping becomes a trivial SQLite query; serve boot untouched; update `internal/e2e/harness_test.go` to drop the Postgres testcontainer and use a temp SQLite file (keep MinIO); acceptance: `go test -tags=integration ./internal/e2e/` green with only MinIO containers
- [x] 3.3 One-time migration command `cmd/pgmigrate`: move pages, assets, counters from Postgres (pgx stays as a dependency of this command only) into SQLite, counters copied exactly; acceptance: command test runs against a seeded Postgres testcontainer (integration tag) and asserts rows + counter values land in SQLite unchanged

## 4. Docs and deployment

- [x] 4.1 Update `docker-compose.yml` (drop postgres service), README config table (`SQLITE_PATH`, Litestream sidecar env/vars), AGENTS.md invariants (pgx → SQLite + Litestream, single admin writer), and add a deployment note: Litestream config sample with replica under `_db/`, `litestream restore -if-db-not-exists` pre-boot step, single-writer rule, restore-window ops note ("UI and bucket disagree → re-run the toggle"); acceptance: docs reviewed against config.go — every documented variable exists
- [x] 4.2 Sweep for stale references: `grep -ri "pgx\|postgres\|DATABASE_URL" --include="*.go" --include="*.md"` outside archive/ and `cmd/pgmigrate` returns only intentional hits; acceptance: list reviewed, none unintentional

## 5. Full verification

- [x] 5.1 Gates per AGENTS.md: `gofmt -l .` empty, `go vet ./...` and `go vet -tags=integration ./...` clean, `go test ./...` green, `go test -tags=integration ./...` green (MinIO only); acceptance: all four pass locally
