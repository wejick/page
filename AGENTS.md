# AGENTS.md

Internal and development documentation for the Page repo — for coding agents
and developers. What the product does and how to run/deploy it: see
[README.md](README.md).

Module `page`. Go, stdlib-first: `net/http` ServeMux with 1.22 pattern
routing (no router/web framework), pgx/v5 for Postgres (write-side only, no
ORM), minio-go behind the storage seam, testcontainers-go for integration
tests.

## Repo layout

```
cmd/server      one binary, mode-gated boot: runServe (DB-free) vs runAdminAll
cmd/seed        uploads a sample Framer-style pack (also re-runs migrations)
internal/config env parsing + per-mode validation (SERVER_MODE: serve/admin/all)
internal/db     migrations (embedded SQL, advisory-locked) + write-side queries
internal/ingest scan → classify → fetch → bake → rewrite (the pipeline)
internal/upload POST/GET /api/pages handlers, bearer auth, manifest
internal/lifecycle park/unpark toggle (_parked/{slug}/ prefix moves) + API
internal/serve  mode-scoped router, URL→key arithmetic, HTML cache, StorageProbe
internal/slug   slug assignment
internal/storage the ONE seam: Put/Get/Stat/Copy/DeletePrefix
  mem/          in-memory driver (unit tests)
  s3compat/     S3-compatible driver (dev + prod)
  storagetest/  conformance suite shared by drivers
internal/e2e    end-to-end tests (//go:build integration, testcontainers)
```

The serve path never touches the database: URL → storage key arithmetic,
content types from object metadata, entry HTML cached in memory and
revalidated via `Stat` after the TTL. Postgres is write-side bookkeeping
only (slug counters, manifest, lifecycle status).

## Build and test

```bash
make up / down           # dev infra: MinIO + Postgres via docker compose
make run / seed          # server (:8080) / sample pack upload
make test                # unit tests, no Docker needed
make test-integration    # go test -tags=integration ./... (needs Docker)
make tidy
```

Before declaring done: `gofmt -l .` empty, `go vet ./...` and
`go vet -tags=integration ./...` clean, `go test ./...` green, and — when
the change touches runtime behavior — `go test -tags=integration ./...`
green (real MinIO + Postgres via testcontainers).

Testing rules: unit tests run against the `mem` driver and fixture packs on
disk; integration tests use real containers. Mock the external asset
fetcher's HTTP and nothing else — never our own interfaces. Tests are
table-driven and live next to the code (e2e tests in `internal/e2e`).

## Configuration (full)

| Variable | Dev | Prod | Notes |
|---|---|---|---|
| `SERVER_MODE` | `all` (default) | `all`, or `serve` + `admin` when split | `serve` pages/assets only · `admin` upload UI + API only · `all` everything |
| `DATABASE_URL` | `postgres://page:page@localhost:5432/page` | managed Postgres | required unless `SERVER_MODE=serve` |
| `AUTH_TOKEN` | `devtoken` | secret | bearer token on `/api/*`; required unless `SERVER_MODE=serve` |
| `STORAGE_DRIVER` | `s3compat` | `s3compat` | or `mem` (tests) |
| `S3_ENDPOINT` | `localhost:9000` | vendor endpoint | required for s3compat |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | minioadmin | secret | required for s3compat |
| `S3_BUCKET` | `pages` | `pages` | |
| `S3_SECURE` | `false` | `true` | https to endpoint |
| `S3_PATH_STYLE` | `true` | vendor-dependent | MinIO needs `true` |
| `ADDR` | `:8080` | `:8080` | |
| `HTML_CACHE_TTL` | 60s | 60s | entry-HTML cache revalidation window; parked pages stop serving within it |
| `UPLOAD_MAX_RAW_BYTES` | 25 MiB | … | upload caps below keep ingest synchronous |
| `UPLOAD_MAX_DECOMPRESSED_BYTES` | 100 MiB | … | zip-bomb guard |
| `UPLOAD_MAX_FILES` | 2000 | … | |
| `ASSET_MAX_BYTES` | 10 MiB | … | per asset (zip entries and fetches) |
| `FETCH_TIMEOUT` / `FETCH_BUDGET` | 10s / 60s | … | per fetch / per upload |
| `FETCH_CONCURRENCY` | 8 | … | |
| `KEEP_EXTERNAL_FONTS` | fonts.googleapis.com, fonts.gstatic.com, use.typekit.net | … | comma lists, per category |
| `KEEP_EXTERNAL_JS` | cdn.jsdelivr.net, unpkg.com, cdnjs.cloudflare.com, esm.sh | … | |
| `KEEP_EXTERNAL_ICONS` | use.fontawesome.com | … | |
| `KEEP_EXTERNAL_MISC` | www.googletagmanager.com, plausible.io | … | |

Storage works with any S3-compatible endpoint (AWS S3, R2, B2, Spaces,
MinIO, …), configured entirely via env. Migrating providers is
`rclone copy old new` plus an env change — objects are immutable and flat
under `{slug}/`.

## Deployment

**Requirements:** one DNS name (e.g. `page.mycompany.com`) and a TLS
certificate for it.

**CDN in front (production):** a single distribution on that hostname with
path-based behaviors:

| Path | Origin | Notes |
|---|---|---|
| `/p/*`, `/a/*` | **bucket** (direct) | edge rewrites `/p/{slug}/…` and `/a/{slug}/…` to bucket key `{slug}/…`; sets `X-Content-Type-Options: nosniff`; entry HTML uses short TTL + ETag revalidation, assets cache with the immutable headers the app set |
| `/`, `/api/*` | Go service | upload, ingest, API, park/unpark, UI |

Missing keys must map to **404** at the edge: S3's REST endpoint returns
**403** for missing keys on some code paths — translate both to 404.
After parking a page, purge `/p/{slug}/*` and `/a/{slug}/*` for
edge-level removal.

**One binary, two modes:** `SERVER_MODE` picks which planes an instance
mounts: `serve` (pages/assets only), `admin` (upload UI + API only), or
`all` (default — everything). An invalid value fails startup. The
two-instance split is opt-in and purely orchestration: the same image runs
twice, with the edge sending `/p/*` and `/a/*` to the serve instance while
`/` and `/api/*` go to the admin one — the path table above is the URL
contract either way.

- **Serve instance (public tier):** storage settings only — no
  `DATABASE_URL`, no `AUTH_TOKEN`, no Postgres connection ever opened.
  Mounts `/p/*`, `/a/*`, `/healthz` only; `/` and `/api/*` are 404, so
  upload/ingest can't be triggered from the public tier. Its `healthz` is a
  storage `Stat` (any response, even not-found, counts as healthy), so a
  Postgres outage can't fail serving health. Give it **read-only storage
  credentials** — serving never writes.
- **Admin instance (private tier):** storage settings plus `DATABASE_URL`
  and `AUTH_TOKEN`. Mounts `/` and `/api/*` only (`/p/*`, `/a/*` are 404);
  `healthz` pings Postgres. Boot duties live here — migrations, bucket
  creation, the lifecycle sweep. Restrict it to the internal network/VPN;
  only the edge needs to reach it.

Per-instance env matrix (values and defaults as in the configuration table
above; an `all` instance needs everything):

| Variable | serve | admin | all (default) |
|---|---|---|---|
| `S3_ENDPOINT` / `S3_ACCESS_KEY` / `S3_SECRET_KEY` | required (read-only key) | required | required |
| `STORAGE_DRIVER`, `S3_BUCKET`, `S3_SECURE`, `S3_PATH_STYLE` | optional (defaults) | optional (defaults) | optional (defaults) |
| `DATABASE_URL`, `AUTH_TOKEN` | n/a — leave unset | required | required |
| `ADDR`, `HTML_CACHE_TTL` | optional | optional | optional |
| `UPLOAD_*`, `ASSET_MAX_BYTES`, `FETCH_*`, `KEEP_EXTERNAL_*` | n/a — no upload path | optional | optional |

**Adopting the split:** deploy `SERVER_MODE=all` (or unset) everywhere
first — behavior identical to today — then add the serve instance and flip
the edge's `/p/*`, `/a/*` origins to it. Rollback is a mode flip back to
`all` (or the previous image); no schema or storage-format changes are
involved.

## How it works

```
upload ──▶ ingest (scan · classify · bake · rewrite) ──▶ bucket {slug}/…
serve  ──▶ URL→key arithmetic (no DB) ──▶ cached bytes
```

- **Ingest is the only smart part** (design: all intelligence once, at
  upload): safe unzip → entry detection (root `index.html`, else shallowest
  `.html`, stored as `{slug}/index.html`) → reference scanning (HTML attrs,
  srcset, style blocks/attrs, SVG, CSS `url()`/`@import`/`@font-face`,
  recursive) → classification (signed URLs and unknown hosts bake;
  allowlisted CDNs stay external; everything else bakes) → bounded
  concurrent fetches → refs rewritten to origin-absolute `/a/{slug}/…`.
- **Failures degrade gracefully:** an asset that can't be fetched keeps its
  original URL and is recorded `kept-external` in the manifest — an accepted
  compromise, visible via `GET /api/pages/{slug}`, never an upload error.
  Manifest statuses: `local`, `baked`, `kept-cdn`, `kept-external`.
- **Takedown without serve-plane logic:** parking moves objects under the
  reserved `_parked/{slug}/` prefix; serving learns about it through key
  existence, and the entry-HTML cache revalidates via `Stat` after the TTL.
  A crash mid-toggle is resumed by the lifecycle `Sweep` on admin/all boot.
  When a CDN fronts the bucket, purge `/p/{slug}/*` and `/a/{slug}/*` after
  parking for edge-level removal.
- **One binary, mode-gated:** `SERVER_MODE=serve` boots from validated
  storage config only — no pool, no migrations, no bucket create, no sweep;
  its health probe is a storage `Stat`. `admin`/`all` run the boot duties;
  `db.Migrate` takes a Postgres advisory lock so concurrent admin replicas
  migrate safely.
- **Vendor neutrality:** the entire storage surface is four operations
  (`Put`, `Get`, `Stat`, `DeletePrefix`); drivers are interchangeable and
  conformance-tested. Production vendor deliberately undecided.

## Engineering principles (enforced)

Full statement in `openspec/config.yaml` (`context:`). The short version:

- Ship the smallest thing that satisfies the spec — no speculative
  abstraction, no config knob without a scenario, no dependency without a
  direct consumer. Every addition traces to a requirement.
- The only code seam is `internal/storage`'s interface (multiple real
  implementations justify it). BANNED beyond it: clean/onion/hexagonal
  architecture, interfaces over a single implementation, repository/service
  indirection, DI containers/factories, port/adapter seams, barrel files.
  Code is flat and concrete: functions import and call what they use.
- gofmt + go vet clean; errors wrapped with `%w` (package-prefixed:
  `db: …`, `storage: …`); `context` propagated on every call that does I/O.

## Specs and changes (OpenSpec)

Normative behavior lives in `openspec/specs/<capability>/spec.md` (every
requirement has testable scenarios). Work is specified as changes in
`openspec/changes/<name>/`: `proposal.md` (why/what, mandatory non-goals),
`design.md` (decisions with alternatives considered), `tasks.md` (small
task graph, each task paired with its tests), and spec deltas under
`specs/`.

```bash
openspec validate <change-name>        # before implementing/archiving
openspec archive <change-name> --yes   # syncs deltas into openspec/specs/,
                                       # moves the change to changes/archive/
```

Code comments cite design decisions as `(Dn)` — numbered per the design
doc that introduced them; decisions from the split-serve-admin-deployment
change are cited as `(deployment-modes Dn)` to avoid clashing with the
legacy numbering from the static-page-hosting archive.
