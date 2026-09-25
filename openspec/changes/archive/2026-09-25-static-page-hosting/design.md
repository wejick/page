## Context

Greenfield Go service (see proposal.md). The team publishes static pages built in Figma/Framer or captured via browser "Save As". Sources differ (single HTML with CDN links vs zip packs with local assets), but the requirement is uniform: upload a pack, get a URL that keeps working. Audience is the internal team; pages are immutable artifacts. The storage vendor is deliberately undecided, so storage access must be vendor-neutral. Engineering principles from openspec/config.yaml apply: no over-engineering, one justified seam (Storage), tests paired with every task.

## Goals / Non-Goals

**Goals:**

- Upload (single HTML or zip) → working URL in one synchronous step.
- Pages are self-contained: assets fetched into our storage at ingest, refs rewritten.
- Serving is DB-free arithmetic + cached bytes — no logic in the hot path.
- Vendor-neutral storage behind exactly one seam; MinIO locally, any S3-compatible store in prod.
- Single origin, path-based URLs — IT grants one name + one cert, done.
- Ingest failures degrade gracefully; they never block an upload.

**Non-Goals:**

- Page updates/versioning, custom domains, public sign-up, multi-tenancy.
- SSO, malware scanning, async ingest, runtime-JS rewriting.
- Any interface or indirection layer beyond the Storage seam (config.yaml principles).

## Decisions

### D1: One origin, path-based serving (`/p/{slug}/`, `/a/{slug}/`)

**Alternatives considered:** wildcard subdomains (`{slug}.page...`) — rejected: wildcard DNS + DNS-01 TLS automation is a much heavier IT ask, and D4's rewriting makes subdomains buy nothing; separate asset hostname — rejected: cross-origin `@font-face` breaks without CORS config (the classic "everything works except fonts" bug); same-host path prefixes have neither problem.

### D2: Bake at ingest; keep the hot path dumb

All intelligence (unzip, scan, fetch, rewrite) runs once, at upload. Serving is arithmetic plus cached bytes.

**Alternatives considered:** rewriting at serve time — rejected: reintroduces per-request computation and cache invalidation, exactly what immutability avoids; storing packs as-is — rejected: silent rot when source CDNs change (the failure that motivated the service).

### D3: Best-effort baking with incentive-based classification

Classification order per external ref: (1) signed/expiring query params (`Expires=`, `X-Amz-Signature`, `token=`, `sig=`) → bake — rotting by definition; (2) host on the keep-external allowlist (config knob, categorized fonts/js/icons/analytics — serves the `ingest-pipeline` classification requirement) → keep external, record `kept-cdn`; (3) everything else → bake (default — unnecessary baking is cheap, wrong keeping is silent rot). Fetch failures never fail the upload: original URL kept, recorded `kept-external` — an accepted compromise, visible in the manifest, not a warning.

**Alternatives considered:** bake-everything — rejected: wastes upload latency and drops browser cross-site caches for CDNs more durable than ours; keep-everything — rejected: unknown hosts rot silently; block upload on fetch failure — rejected: an SSO-walled asset must not prevent publishing.

### D4: Refs rewritten to origin-absolute paths (`/a/{slug}/...`)

Same-origin serving means zero CORS configuration. Path-absolute form is host-independent — the domain can change without touching stored content.

**Alternatives considered:** full absolute URLs (scheme+host) — rejected: strand content if the host ever changes; relative-only refs — rejected: break under any path restructure; leaving Figma/Framer URLs unrewritten — rejected: that is the rot we're defending against.

### D5: Asset dual-mount for runtime-constructed refs

Assets serve under both `/a/{slug}/*` and `/p/{slug}/*` (same bucket prefix, routing alias only): relative refs built by JS at runtime resolve against the page path and still find bytes. Zero extra storage. In prod the CDN's path rewrite implements the mapping; in dev the Go proxy does — identical URL contract.

**Alternatives considered:** single mount — rejected: "works except one image inside a JS carousel" bugs; scanning/rewriting JS — rejected: statically impossible.

### D6: Slugs are `{identifier}-{counter}`, stored as separate columns

`pages.identifier` (sanitized) + `pages.code` (int, atomic upsert `RETURNING`, per-identifier, starts at 1) + `pages.slug` (unique index backstop). Always append, never parse — `landing-page-1-2` is a valid opaque identifier. Sanitization: lowercase, `[a-z0-9-]`, trimmed/collapsed dashes, length cap. Reserved: `api`, `a`, `p`, `ui`, `www`, `assets`, `cdn`, `static`, `healthz`.

**Alternatives considered:** random nanoid slugs — rejected: `landing-page-3` is speakable and grep-able, which matters internally; parsing slugs at serve time — rejected: fragile, and pointless once parts are columns.

### D7: Vendor-neutral storage seam — the one interface

`Storage` with `Put` / `Get` / `Stat` / `DeletePrefix`. Requirement served: the storage vendor is an open decision (explicit project constraint), and the seam has two real implementations — `s3compat` (minio-go: one client, any S3-compatible endpoint — AWS, MinIO, R2, B2, Spaces, Wasabi, GCS-interop) and `mem` (unit tests). It is the only interface in the codebase; everything else calls pgx, minio-go, and net/http directly. Frozen at four ops — no List, no multipart, no presigned URLs (assets serve via CDN→bucket in prod, Go proxy in dev). Driver, endpoint, bucket via env. DeletePrefix may use list+batch-delete internally — inside the driver, not the interface.

**Alternatives considered:** aws-sdk-go-v2 — rejected: AWS-branded and heavier, against the vendor-open requirement; gocloud.dev — rejected: maintenance-mode and its S3 driver drags the AWS SDK in anyway; calling minio-go without a seam — rejected: breaks vendor-openness; a `file` driver — **cut during config adoption**: no scenario consumes it (unit tests use `mem`, integration uses real MinIO), so it violated no-over-engineering.

### D8: Postgres, write-side only (pgx/v5)

`pages(slug UNIQUE, identifier, code, asset_count, total_bytes, created_at)` · `assets(slug, path, source_url, content_type, bytes, status)` · `counters(identifier, next)`. No page bytes in the DB; entry HTML lives in the bucket at `{slug}/index.html`. Dependency serves: slug counters, manifest, upload API. No ORM — pgx direct.

**Alternatives considered:** `html_bytes`/`entry_key` on `pages` — rejected during config adoption review: couples every page view to Postgres for zero benefit (D13 removes the need); SQLite — rejected: the manifest/counter writes want a real server, and pgx/testcontainers are already justified.

### D9: Auth is a static bearer token in a header

Internal tool. Header-based (never cookie) so JS inside uploaded pages cannot forge uploads even though pages share the origin with the API. Serves the `page-upload` auth requirement.

**Alternatives considered:** SSO/OIDC — non-goal for v1; cookie sessions — rejected for the forging reason above.

### D10: Bounded, synchronous ingest

Caps: max raw size, max decompressed size, max file count, max per-asset size, total fetch time budget, small fetch concurrency pool (config knobs — each serves the bounded-ingest requirement; none exist without a scenario). Within caps, upload responds with the finished page.

**Alternatives considered:** async ingest + status polling — rejected: UI and state-machine complexity no internal scenario needs; unbounded ingest — rejected: one huge pack could pin the service.

### D11: Zip safety at ingest

Reject: `..` or absolute entry paths, symlink entries, nested zips, oversized entries, no-HTML packs. Entry = root `index.html`, else shallowest `.html`. Fetched/zip bytes are content-type sniffed before `Put` (serve path trusts object metadata, so ingest must set it truthfully).

**Alternatives considered:** trusting internal uploaders — rejected: traversal and zip bombs are one `if` each to block and catastrophic to miss.

### D12: Local dev = MinIO + Postgres in docker compose

Same `s3compat` driver in dev (endpoint `minio:9000`) as prod — the tested path is the shipped path. testcontainers-go boots real MinIO + Postgres per integration run. `make seed` uploads a sample pack.

**Alternatives considered:** a fake storage in dev — rejected: wouldn't exercise the prod code path; testcontainers-only (no compose) — rejected: kills the interactive dev loop.

### D13: DB-free serve path by normalization, not caching

Two ingest normalizations make URL → key arithmetic: (1) entry HTML always stored as `{slug}/index.html`; (2) content types in object metadata, returned by `Get` with the bytes. Then `/p/{slug}/rest` → `{slug}/rest`, `/p/{slug}/` → `{slug}/index.html`, `/a/{slug}/rest` → `{slug}/rest`. Zero database queries on any serve path; 404 = storage miss. DB is write-side bookkeeping. `rclone` migrates all bytes, since HTML is in the bucket too.

**Alternatives considered:** metadata lookups plus a caching layer — rejected: more moving parts than arithmetic for the same outcome; content-type from extension at serve time — rejected: lies about sniffed types and splits the source of truth.

### D14: In-memory HTML cache + CDN serving split

`htmlCache` (slug → entry bytes, lazy-fill, byte-budget LRU as a safety valve): repeat page views never touch storage. No TTLs, no invalidation — content is immutable. No negative caching of misses (a page created after a miss must serve). Production topology: CDN fronts the single hostname — `/p/*`, `/a/*` → bucket origin with edge path-rewrite to `{slug}/…` (dual-mount preserved), nosniff header, missing-key → 404 (S3 REST returns 403 for missing keys; translated at the edge); `/`, `/api/*` → Go origin. Go is an upload/admin service in prod; its byte proxy is the dev stand-in with the identical URL contract.

**Alternatives considered:** a metadata/registry cache (`pageReg`) alongside `htmlCache` — **cut during config adoption**: no requirement consumes it (the admin API reads Postgres directly at its trivial traffic level), so it violated no-over-engineering; proxying assets through Go in prod — rejected: defeats the CDN; no HTML cache — rejected: a storage roundtrip per view for data that fits in RAM is pure waste.

## Risks / Trade-offs

- [Runtime-JS refs invisible to the scanner] → dual-mount serving (D5); residual gaps fixed case-by-case.
- [SSO-walled internal assets can't be fetched at ingest] → kept-external keeps them working for corp viewers; visible in manifest.
- [Baking slows uploads] → caps + fetch pool bound it; classification skips stable CDNs entirely.
- [The baker fetches whatever HTML points at (SSRF-shaped power)] → acceptable for trusted internal uploaders; size/time caps are the guardrails.
- [minio-go vs a future vendor quirk] → seam is 4 methods; a driver change is contained by construction.
- [kept-external refs rot later] → accepted compromise by policy; manifest makes it queryable.
- [Edge path-rewrite / 404 mapping misconfigured in prod] → integration tests assert the same-URL contract (dual-mount equivalence, slashless redirect, 404s) for both the dev proxy and the edge config.
- [Counter race on same identifier] → atomic upsert + unique index; retry-on-conflict backstop.

## Migration Plan

Greenfield: deploy service, run schema migration, create bucket, configure the DNS name + cert from IT. Rollback = remove deployment; stored objects are immutable and `rclone`-portable to any future storage. No existing system is affected.

## Open Questions

- Production storage vendor — deliberately open; D7's seam makes it a config change.
- Which CDN fronts the bucket (CloudFront or corporate alternative) — follows the storage decision; config-only.
- Optional nicety: `GET /p/{identifier}` → 302 to highest code ("latest" alias) — deferred unless a scenario demands it.
