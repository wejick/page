## Verification: replace-postgres-with-sqlite (2026-10-02)

**Promise:** after this change, an operator runs the whole service with a
local SQLite file (no Postgres server anywhere) while every user-facing
behavior — upload, list, park/unpark, delete, serving, auth — works
identically, and the database survives restarts and fresh-host boots.

**Verdict:** delivered with findings

| Journey | Exercised intent | Verdict | Evidence |
|---|---|---|---|
| J1 upload → view (UI + API) | page-upload, ingest-pipeline, page-serving | pass | evidence/j1-page-render.png; /tmp/…/j1-upload2.json |
| J2 park/unpark via UI + cache window | page-lifecycle, admin-ui | pass | UI transcripts in session; curl transcripts below |
| J3 delete + code-never-reused | page-lifecycle, page-management | pass | curl transcripts |
| J4 mode contract (serve w/o SQLITE_PATH, admin) | deployment-modes | pass | curl transcripts |
| J5 restart persistence (SIGTERM → same file) | sqlite-persistence | pass | curl transcripts |
| J6 failure paths (401/404/415/422, fail-fast boot) | deployment-modes, page-upload | pass | curl transcripts |

### Evidence highlights (HTTP transcripts, live :8080 all-mode + :8081 serve + :8082 admin)

- **J1**: zip upload of `index.html`+`style.css` → 201 `{"slug":"qa-sqlite-2-1",…,"local":1}`;
  `/p/qa-sqlite-2-1/` 200 with `href="/a/qa-sqlite-2-1/style.css"`, asset 200; browser
  screenshot shows the stylesheet applied (teal heading, lavender background).
- **J2**: UI Park click → row flips `parked`, Open link gone; `/p/…` **and** `/a/…`
  404 immediately (entry cache revalidation caught the move); UI Unpark → `live`,
  page 200, asset bytes identical (`body{…background:#eef…}`).
- **J3**: DELETE → 200, `/p/qa-sqlite-1-1/` 404; re-upload identifier `qa-sqlite-1`
  → code **2** (`qa-sqlite-1-2`) — code 1 never reused; `?status=parked` filter works.
- **J4**: serve instance booted with `SQLITE_PATH`/`AUTH_TOKEN` stripped from env —
  boots, `healthz` 200 (storage probe), `/p/sample-1/` 200, live asset 200,
  `/api/pages` 404, `/` 404. Admin instance: `healthz` 200, `/p/*` 404, `/a/*` 404,
  `/api/*` 200 (token), `/` 200.
- **J5**: SIGTERM → clean exit; restart on the same `data/page.db` → all 5 pages
  intact (`qa-caf-2-1`, `qa-sqlite-1-2`, `qa-sqlite-2-1`, `qa-sqlite-2-2`,
  `sample-1`), deleted page still gone, parked page still parked (404).
- **J6**: no/wrong token → 401; park/delete unknown slug → 404; empty file → 415
  with a plain-language message; reserved identifier `api` → 422 naming the rule;
  admin boot without `SQLITE_PATH` → fatal `invalid config: SQLITE_PATH is required`.
- **Fresh-boot schema**: first boot created `data/page.db` (+ `-wal`/`-shm`, WAL mode
  confirmed) and migrations applied — pages served immediately after.

### Findings

- [major] **Fresh-clone `make run` fails: "unable to open database file (14)"** —
  the documented dev flow (`make up` then `make run`) points `SQLITE_PATH` at
  `data/page.db`, but nothing creates the `data/` directory and SQLite does not
  mkdir parents; boot dies at open. Expected: `make run` works out of the box
  (README Quick start). Classification: **code bug**. Worked around during
  verification by `mkdir -p data`. Evidence: `evidence/fresh-boot-cantopen.log`.
  **Fixed and re-verified (2026-10-02, scoped re-run):** `db.Open` now creates
  the parent directory (`TestOpenCreatesParentDirectory` covers it); a fresh
  boot with no `data/` present (`rm -rf data && make run`) came up healthy,
  seeded, and served `/p/sample-1/` — see *Scoped re-verify* below.

### Exploratory notes

- Identifier `" QA Café !! 2 "` sanitizes to `qa-caf-2`, uploads and serves —
  sanitation behaves through the real API.
- Re-using an identifier allocates the next code (`qa-sqlite-2` → `qa-sqlite-2-2`)
  — counter state visible through the API after restarts.
- Parking an already-parked page → 200 idempotent. PATCH on a page → 405.
- `/p/_db/x` and `/p/_db/page.db` → 404 on both instances (replica prefix not
  reachable through the page mount).
- Single-HTML upload with a relative stylesheet stores no asset rows (nothing to
  resolve against; pre-existing ingest semantics, unchanged by this change) —
  zip packs are the path with local assets.
- Serve-mode health stayed 200 for the whole run with a SQLite file that the
  serve instance never received — the mode isolation holds in practice.

### Coverage

Delta scenarios left to integration tests (not observable through the product
surface in a single-instance dev run): concurrent same-file migration (unit
tests `TestMigrateConcurrentBoots` + `TestMigrateConcurrentHandles` — the
latter uses two separate handles, as two processes would), counter atomicity
under concurrency (`TestAllocateConcurrent`), Litestream replication/restore
mechanics (orchestration-level; app-side contract = restore-before-boot,
documented), health-503-on-unreadable-DB (the admin probe is now a real
schema query — `db.Ping` — which also catches a corrupt-but-openable file;
a missing/permission-denied file fails at boot), pgmigrate row move
(`TestMovePreservesRowsAndCounters`).

### Scoped re-verify (2026-10-02, after review fixes)

Review fixes applied: fresh-boot mkdir in `db.Open`; `CreatePage` error
wrapping; README `SQLITE_PATH` default row corrected; task 1.1/proposal
wording scoped to keep pgx + the Postgres testcontainer for `cmd/pgmigrate`
only; admin health probe switched from `PingContext` to a real schema query
(`db.Ping`); page-status CHECK vocabulary covered again;
`TestMigrateConcurrentHandles` added (two handles, matching the spec's
two-instance scenario); DSN pragma order hardened (`_busy_timeout`
shorthand first — the driver applies the `_pragma` list lexicographically,
so `journal_mode` ran before the wait budget and could return an instant
SQLITE_BUSY on a concurrent first boot; `Migrate` additionally retries
connection acquisition on BUSY).

Re-run: `rm -rf data && make run` → `healthz` 200, `make seed` → HTTP 201,
`/p/sample-1/` 200, clean SIGTERM. Full gates re-passed: gofmt empty, both
`go vet` runs clean, `go test ./...` green (db suite stable at `-count=25`),
`go test -tags=integration ./...` green.

### Environment left behind

- Stopped: the `make run` server (:8080), the serve (:8081) and admin (:8082)
  verification instances. MinIO (`make up`) left running as found.
- Pre-existing, untouched: a stale `page-postgres-1` container from the old
  compose config is still running (the new compose file no longer defines it) —
  safe to remove with `docker compose down` on the old config or
  `docker rm -f page-postgres-1`.
- Leftover state: `data/page.db` (+WAL) in the repo with pages `sample-1`,
  `qa-caf-2-1`, `qa-sqlite-1-2`, `qa-sqlite-2-1`, `qa-sqlite-2-2` and their
  objects in the MinIO `pages` bucket (runtime data; `make down -v` + delete
  `data/` resets it).
