# Proposal: split-serve-admin-deployment

## Why

Serving and administration have opposite security and availability profiles, but one process owns both: the public tier requires `DATABASE_URL` and `AUTH_TOKEN` at boot, pings Postgres in `healthz`, and mounts the ingest/egress surface — so a DB outage can mark healthy serving instances dead, and a compromise of the public tier exposes admin credentials. The serve path is already DB-free by design; the deployment just doesn't trust that yet.

## What Changes

- New `SERVER_MODE` env (`serve` | `admin` | `all`, default `all`): one binary, deployed as one or two instances. Dev (`all`) is unchanged.
- **Per-mode configuration**: serve mode requires only storage settings; admin mode additionally requires `DATABASE_URL`/`AUTH_TOKEN`; missing vars fail fast at boot. Serve mode opens **no Postgres connection at all**.
- **Plane-scoped routing**: serve instances mount only `/p/*`, `/a/*`, `/healthz` (no `/api`, no upload UI); admin instances mount `/`, `/api/*`; `all` mounts everything as today.
- **Per-mode health**: serve `healthz` probes storage (a `Stat` — any response means healthy); admin/all keep the Postgres ping.
- **Boot duties move to the admin plane**: migrations, bucket creation, and the lifecycle sweep run only on `admin`/`all` boots. `db.Migrate` gains a Postgres advisory lock so concurrent admin replicas boot safely.
- README: two-instance deployment topology, per-instance env matrix, security guidance (read-only storage credentials for the serve tier, admin network restriction).

## Capabilities

### New Capabilities

- `deployment-modes`: mode selection and per-mode boot behavior — required configuration, plane-scoped routing surfaces, per-mode health probes, and safe concurrent admin migrations.

### Modified Capabilities

(none — `page-serving`'s DB-free requirement is preserved and extended operationally by `deployment-modes`; no existing requirement text changes)

## Impact

- `internal/config`: `Mode` field + per-mode validation.
- `internal/serve`: plane-scoped router construction.
- `cmd/server`: conditional boot (no pgxpool, no migrations, no bucket create, no sweep in serve mode; storage health probe).
- `internal/db`: advisory lock in `Migrate`.
- README deployment section.
- No new dependencies; existing e2e/unit suites keep running in `all` mode.

## Non-goals

- Separate container images or CI pipelines (one image, two instance configs).
- CDN/TLS termination, edge routing config, or bucket-direct serving (existing documented contract stands).
- Admin auth upgrades (mTLS/SSO) — bearer token + network restriction guidance only.
- Autoscaling policies and queue-based ingest.
