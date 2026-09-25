# static-page-hosting

Internal self-serve static page publisher. Upload an HTML pack (single HTML
file or zip from Figma/Framer exports, browser save-as), get an immutable URL.
Assets are baked into object storage at ingest so pages outlive their source
tool's CDN; references to well-known stable CDNs (Google Fonts, jsDelivr, …)
are deliberately kept external.

```
upload ──▶ ingest (scan · classify · bake · rewrite) ──▶ bucket {slug}/…
serve  ──▶ URL→key arithmetic (no DB) ──▶ cached bytes
```

## Run book (local development)

Prerequisites: Go, Docker.

```bash
make up      # MinIO (:9000 API, :9001 console) + Postgres, bucket auto-created
make run     # server on :8080 (auth token: devtoken)
make seed    # uploads a sample Framer-style pack (no server needed)
open http://localhost:8080/p/sample-1/
```

Upload your own pack: open `http://localhost:8080/`, enter token `devtoken`,
optional identifier, drop an `.html`/`.zip`. Or via curl:

```bash
curl -H "Authorization: Bearer devtoken" -F "file=@pack.zip" \
     -F "identifier=landing-page" http://localhost:8080/api/pages
# → 201 {"slug":"landing-page-1","url":"/p/landing-page-1/",...}
```

Pages are immutable: every upload creates a new page (`landing-page-1`,
`landing-page-2`, …). Routes: `/` upload UI · `POST /api/pages` ·
`GET /api/pages/{slug}` (metadata + manifest, includes lifecycle `status`) ·
`POST /api/pages/{slug}/park` · `POST /api/pages/{slug}/unpark` ·
`/p/{slug}/` page · `/p/{slug}/*` and `/a/{slug}/*` assets (dual-mount).

**Takedown is a toggle**: parking a page moves its objects under the
reserved `_parked/{slug}/` prefix (restoring moves them back), so parked
pages 404 with no serve-plane logic. Interrupted toggles are resumed at
boot. Parked pages stop being served within the cache TTL
(`HTML_CACHE_TTL`, default 60s); when a CDN fronts the bucket, purge
`/p/{slug}/*` and `/a/{slug}/*` after parking for edge-level removal.

## Tests

```bash
make test                # unit (no Docker)
make test-integration    # real MinIO + Postgres via testcontainers
```

## Configuration (env)

| Variable | Dev | Prod | Notes |
|---|---|---|---|
| `SERVER_MODE` | `all` (default) | `all`, or `serve` + `admin` when split | `serve` pages/assets only · `admin` upload UI + API only · `all` everything; see Deployment |
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

Production storage is deliberately undecided: any S3-compatible endpoint
(AWS, R2, B2, Spaces, MinIO, …) works via env only. Migrating clouds is
`rclone copy old new` + env change — objects are immutable and flat under
`{slug}/`.

## Deployment

**What to ask IT for:** one DNS name (`page.mycompany.com`) + one ordinary
TLS certificate for it. No wildcard DNS, no wildcard cert, no delegation.

**CDN in front (production):** a single distribution on that hostname with
path-based behaviors:

| Path | Origin | Notes |
|---|---|---|
| `/p/*`, `/a/*` | **bucket** (direct) | edge rewrites `/p/{slug}/…` and `/a/{slug}/…` to bucket key `{slug}/…`; sets `X-Content-Type-Options: nosniff`; entry HTML uses short TTL + ETag revalidation, assets cache with the immutable headers the app set |
| `/`, `/api/*` | Go service | upload, ingest, API, park/unpark, UI |

Missing keys must map to **404** at the edge: S3's REST endpoint returns
**403** for missing keys on some code paths — translate both to 404.
The dev Go proxy already satisfies this URL contract; the integration suite
(`internal/e2e`) asserts it.

**One binary, two modes:** `SERVER_MODE` picks which planes an instance
mounts: `serve` (pages/assets only), `admin` (upload UI + API only), or
`all` (default — everything, exactly today's behavior). An invalid value
fails startup. The two-instance split is opt-in and purely orchestration:
the same image runs twice, with the edge sending `/p/*` and `/a/*` to the
serve instance while `/` and `/api/*` go to the admin one — the path table
above is the URL contract either way.

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
  creation, the lifecycle sweep — and a Postgres advisory lock makes
  concurrent admin replicas safe. Restrict it to the internal network/VPN;
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

- **Ingest is the only smart part** (design: all intelligence once, at upload):
  safe unzip → entry detection (root `index.html`, else shallowest `.html`,
  stored as `{slug}/index.html`) → reference scanning (HTML attrs, srcset,
  style blocks/attrs, SVG, CSS `url()`/`@import`/`@font-face`, recursive) →
  classification (signed URLs and unknown hosts bake; allowlisted CDNs stay
  external; everything else bakes) → bounded concurrent fetches → refs
  rewritten to origin-absolute `/a/{slug}/…`.
- **Failures degrade gracefully:** an asset that can't be fetched keeps its
  original URL and is recorded `kept-external` in the manifest — an accepted
  compromise, visible via `GET /api/pages/{slug}`, never an upload error.
  Manifest statuses: `local`, `baked`, `kept-cdn`, `kept-external`.
- **The serve path is DB-free arithmetic**: URL → storage key, content types
  from object metadata, entry HTML cached in memory and revalidated via
  `Stat` after the TTL. Postgres is write-side bookkeeping only (slug
  counters, manifest, lifecycle status).
- **Vendor neutrality:** the entire storage surface is four operations
  (`Put`, `Get`, `Stat`, `DeletePrefix`) with an S3-compatible driver
  (minio-go) and an in-memory driver for tests.
