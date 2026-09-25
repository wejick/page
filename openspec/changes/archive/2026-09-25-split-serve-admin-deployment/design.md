# Design: split-serve-admin-deployment

## Context

`cmd/server` boots one process that mounts every plane: it opens a pgxpool, runs migrations, creates the bucket, sweeps lifecycle state, and serves `/`, `/api/*`, `/p/*`, `/a/*`, `/healthz`. `config.Load` hard-requires `DATABASE_URL` and `AUTH_TOKEN`; `healthz` pings Postgres. Yet the serve path (`/p/*`, `/a/*`) never touches the database — it is pure URL→key arithmetic over object storage with an in-memory entry cache. The deployment therefore carries admin credentials into the public tier and couples serving availability to Postgres for no functional reason.

The park/unpark feature made the separation concrete: lifecycle code is admin-plane-only, and the serve plane learned about it through key existence.

## Goals / Non-Goals

**Goals:**
- Deployable as two independent instances (serve, admin) from one artifact, with the serve tier holding no database credentials and no admin token.
- Serving availability decoupled from Postgres: no DB at boot, no DB in health.
- Ingest work (egress fetches, zip processing) isolated on the admin plane; the public tier cannot trigger it.
- Dev experience unchanged: default mode runs everything on one port as today.
- Concurrent admin replicas migrate safely.

**Non-Goals:**
- Separate images/CI; CDN/TLS/edge config; admin auth upgrades; autoscaling; queue-based ingest.

## Decisions

### D1: One binary with `SERVER_MODE` (`serve` | `admin` | `all`, default `all`)

Mode gates both configuration requirements and router construction. `all` preserves today's dev behavior exactly.

- *Serves:* `deployment-modes` (mode selection; per-instance deployment from one image).
- **Alternatives considered:** two `cmd/` binaries (credential isolation is structural, but dev must orchestrate two processes and CI builds two artifacts for no runtime benefit); separate docker images (pure ops duplication of one flag).

### D2: Serve mode opens no database resources at all

`cmd/server` opens the pgxpool, runs `db.Migrate`, `EnsureBucket`, and the lifecycle sweep **only** in `admin`/`all` modes. Serve mode validates that storage config exists and boots straight to the router. Database misconfiguration in serve mode is therefore impossible, not merely unused.

- **Alternatives considered:** open the pool lazily (keeps the dependency alive and health coupling semi-intact); keep migrations on serve boots behind a flag (splits ownership of boot duties across both planes — worse).

### D3: Plane-scoped mux construction

`serve.New` takes the mode and mounts only the plane's routes: serve → `/p/*`, `/a/*`, `/healthz`; admin → `/`, `/api/*`, `/healthz`; all → everything. The public tier has no upload/ingest code mounted, so it cannot be triggered, and the admin tier has no page-serving cache to tune.

- **Alternatives considered:** one mux plus edge-path routing (the app-level guarantee disappears; misrouted requests hit admin endpoints on the public tier).

### D4: Health probes per mode

Serve mode: `healthz` runs a storage `Stat` on a probe key — any response (including `ErrNotFound`) proves the storage round-trip works; transport failure is unhealthy. Admin/all: Postgres ping as today.

- *Serves:* serving availability independent of Postgres (a DB outage cannot fail serving instances' health).
- **Alternatives considered:** no-op health in serve mode (lies to the orchestrator); DB ping everywhere (the coupling this change removes).

### D5: Advisory lock around migrations

`db.Migrate` takes `pg_advisory_lock` before the version check and releases it after applying (session-scoped via the pool connection). Two admin replicas booting concurrently serialize; the loser re-checks and skips applied migrations. Also future-proofs the sweep-on-boot model.

- *Serves:* `deployment-modes` (multiple admin replicas).
- **Alternatives considered:** unique-violation-on-insert as the race guard (second replica fails boot and relies on the orchestrator retry — noisier for the same guarantee); run migrations in a separate job (new ops surface for a two-line lock).

### D6: Credentials and topology guidance (docs, not code)

README documents the two-instance topology: serve tier with read-only storage credentials, no DB/token; admin tier with full credentials, reachable only on the internal network/VPN; the existing single-hostname edge contract remains the baseline URL surface. Code enforces what it can (config validation, plane scoping); network placement is operational.

- **Alternatives considered:** enforce read-only credentials in code (the storage API cannot distinguish a read-only grant from a denied write until first write — a probe write per boot is not worth it); second internal hostname as a code-level requirement (pure ops choice, documented as recommended).

## Risks / Trade-offs

- [Two modes double the config matrix] → per-mode validation with explicit failure messages; `all` default keeps dev friction zero.
- [Advisory lock held on a pooled connection] → lock and unlock run on one explicitly acquired connection; documented pattern, released in the same boot path even on migration failure (deferred unlock).
- [Serve tier still caches entry HTML in memory during parks] → already handled by the TTL revalidation shipped in `add-page-park-toggle`; unchanged here.
- [Someone deploys two `all` instances] → behaves exactly like today (harmless; advisory lock makes concurrent migrations safe).

## Migration Plan

1. Deploy the new image with `SERVER_MODE=all` (or unset) everywhere first — behavior identical to today.
2. Split at the orchestrator level: second instance with `SERVER_MODE=serve`, storage-only (read-only) credentials; flip edge routing for `/p/*`, `/a/*` to it (or keep single instance — the split is opt-in).
3. Rollback: redeploy old image or set mode back to `all`; no schema or storage format changes ship in this change (advisory lock is session-scoped, leaves no state).

## Open Questions

- Storage probe key for serve health: a fixed unlikely key (e.g. `_health/probe`, normally absent → `ErrNotFound` = healthy) — default chosen; adjustable in review.
