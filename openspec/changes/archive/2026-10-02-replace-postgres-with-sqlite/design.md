## Context

Postgres is write-side bookkeeping only (design D13): 3 tables (`pages`, `assets`, `counters`), ~7 queries across `db`, `slug`, `lifecycle`, and 3 trivial migrations. The serve plane never touches it, so the swap cannot affect serving or the edge contract. The real Postgres features in use: `$n` placeholders, `TIMESTAMPTZ/now()`, `ANY($3)` array matching, the 23505 SQLState check, and the boot advisory lock — none architectural.

The workload is a handful of writes per day from one admin plane. Postgres buys nothing at that scale but costs a container (dev, CI testcontainers) and currently has no backup story at all: losing the host loses the DB while the bucket keeps serving — every page becomes an unmanageable ghost.

Decisions below were explored with the operator; D1–D6 are settled.

## Goals / Non-Goals

**Goals:**
- One less stateful service: the DB becomes a single WAL-mode file next to the app.
- A real recovery story: WAL replicated to the same S3 bucket, restore-based failover.
- Faster, simpler tests: DB logic testable without Docker.
- Serve mode byte-for-byte unchanged.

**Non-Goals:**
- Multi-writer admin / active-active; sub-second RPO engineering; dual-write migration period; changes to the storage seam, ingest, serving, or edge contract.

## Decisions

**D1 — Driver: `modernc.org/sqlite` (pure Go).** No cgo keeps scratch-image builds and cross-compilation trivial; performance is far beyond this workload. PRAGMAs are per-connection, set via `_pragma=` DSN params on open: `journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(1)`, `synchronous(NORMAL)` (durable on process crash; power-cut durability is delegated to Litestream shipping the WAL). *Alternatives: `mattn/go-sqlite3` (cgo build burden, no benefit here); embedded Postgres (defeats the purpose).*

**D2 — Litestream as a sidecar binary, replica in the same bucket.** The sidecar owns replication and restore tooling (`litestream replicate` / `litestream restore -if-db-not-exists`); the Go binary does not embed it. The replica lives under `_db/` in the existing bucket — safe by construction (slugs cannot start with `_`, sanitization excludes underscores) and unreachable from the public edge, which only maps `/p/*` and `/a/*`. One endpoint, zero new infrastructure. Litestream config lives beside the deployment, not in `internal/config`. *Alternatives: in-process litestream library (dilutes its stability into our build, moves restore tooling onto us); custom `VACUUM INTO` + `storage.Put` snapshot (~100 lines, zero deps, philosophically closest to the repo — rejected because it forfeits sub-second RPO and mature restore tooling for savings this repo can afford less than it can afford a second binary); separate bucket (more surface, no benefit). Serves: the recovery requirement.*

**D3 — Single admin writer, restore-based failover.** Postgres-as-shared-service is what made "concurrent admin replicas" possible; a file cannot be shared across hosts. The contract becomes: at most one admin writer; two processes booting against the *same file* stay safe (SQLite's write lock serializes migration — see D4); two hosts must not write simultaneously. Failover = `litestream restore` on a new host, then start admin. This amends the deployment-modes concurrent-boots requirement. *Alternatives: keep Postgres (status quo); distributed SQLite (absurd at this scale).*

**D4 — Migrations inside one `BEGIN IMMEDIATE` transaction.** Replaces the advisory-lock ceremony entirely: the transaction takes SQLite's write lock, the loser blocks on `busy_timeout`, then re-reads `schema_migrations` and skips. The version-check loop stays. *Alternatives: keep a lock table + retry loop (more code for less guarantee); file locks (SQLite already is one).*

**D5 — SQL dialect deltas.** `$n` → `?`/`?NNN` (`?1` keeps `ListPages`'s one-query-shape trick: `WHERE (?1 IS NULL OR status = ?1)`); `TIMESTAMPTZ now()` → `INTEGER` unix **millis** with `DEFAULT` set in code (preserves Postgres' sub-second `created_at` ordering before the slug tiebreak); `status = ANY($3)` → `IN (?,?,?)`; `slug.IsUniqueViolation` → driver error-code check (`sqlite.Error` with `Code == 2067`/`SQLITE_CONSTRAINT_UNIQUE`) keeping the upload retry loop's shape. `ORDER BY path` shifts from locale collation to BINARY — observable only for non-ASCII asset paths. *Alternatives: seconds-granularity timestamps (loses ordering information the schema previously had).*

**D6 — Replication window accepted and documented.** The bucket is written synchronously by the app; Litestream ships WAL asynchronously (≤~1s). A crash in the window restores a DB that is behind the bucket: an uploaded page serves but has no row (and its counter may reallocate the slug); a parked page can be restored as `live` with dark keys (converges when the toggle is re-run). This window exists today with infinite width — there is no Postgres backup at all — so Litestream shrinks it, and the serve plane's blindness to the DB is unchanged. Accepted as a visible compromise like `kept-external`, with the ops note: if UI and bucket disagree, re-run the toggle; it converges. Sweep semantics are untouched. *Alternatives: fsync/sync-point before acknowledging lifecycle ops (engineering cost for a window that is already better than status quo).*

**D7 — Config: `SQLITE_PATH` replaces `DATABASE_URL`.** Required for admin/all, validated fail-closed like every other variable; ignored (and not required) in serve mode, which still opens no database resources. One-time pg→sqlite row move is a small `cmd` one-off, not a dual-write path; counters copied exactly (codes never move backward). *Alternatives: keep `DATABASE_URL` and parse a sqlite DSN out of it (confusing); no config (path must be deployable).*

## Risks / Trade-offs

- [Two admin hosts accidentally write one file via shared volume] → Single-writer rule is normative + README deployment note; same-file races remain safe, cross-host divergence is the operator's to avoid.
- [Litestream sidecar misconfigured → silent no-replication] → Restore step (`-if-db-not-exists`) is part of boot docs; a fresh admin host restoring from an empty replica is immediately visible as an empty DB.
- [modernc.org/sqlite subtle driver bugs] → DB tests move into the always-run suite (no `-tags=integration`), so coverage increases; workload is trivially small.
- [Litestream writes add `_db/` objects to the page bucket] → Reserved-prefix convention already proven by `_parked/`; edge never exposes bucket root.

## Migration Plan

1. Deploy the new binary + sidecar to a fresh host (no in-place pg→sqlite on the same volume).
2. Run the one-time row move from the existing Postgres (pages, assets, counters) into the new SQLite file while the old admin is stopped.
3. Start admin (restore-if-absent no-ops, migrate no-ops), verify page list matches, then flip any orchestration. Serve instances never restart for this change.
4. Rollback: stop the new admin, keep serving (unaffected); restart the old Postgres-backed admin. Objects written since the cutover need the row move re-run incrementally.

## Open Questions

None — all decisions settled with the operator (D1–D7).
