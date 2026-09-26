# AGENTS.md

Internal and development documentation for the Page repo — for coding agents
and developers. Product overview, quick start, and base configuration:
[README.md](README.md).

Module `page`. Go, stdlib-first: `net/http` ServeMux with 1.22 pattern
routing (no router/web framework), pgx/v5 for Postgres (write-side only, no
ORM), minio-go behind the storage seam, testcontainers-go for integration
tests.

## Design invariants

The patterns and rationale behind the structure; the code may move, these
don't:

- **The serve path never touches the database.** Serving is URL → storage
  key arithmetic, content types from object metadata, entry HTML cached in
  memory and revalidated via `Stat` after the TTL. Postgres is write-side
  bookkeeping only (slug counters, manifest, lifecycle status) — a Postgres
  outage can't affect page serving, and serving health never depends on it.
- **`internal/storage` is the only code seam.** Five operations (`Put`,
  `Get`, `Stat`, `Copy`, `DeletePrefix`), deliberately frozen: no listing,
  no multipart, no presigned URLs. Drivers (`mem`, `s3compat`) are
  interchangeable and conformance-tested; production vendor deliberately
  undecided — migrating providers is `rclone copy old new` plus an env
  change, since objects are immutable and flat under `{slug}/`.
- **Ingest is the only smart part.** All intelligence runs once, at upload
  (scan → classify → fetch → bake → rewrite); serving stays dumb. An asset
  that can't be fetched keeps its original URL and is recorded
  `kept-external` in the manifest — an accepted compromise, visible via
  `GET /api/pages/{slug}`, never an upload error. Manifest statuses:
  `local`, `baked`, `kept-cdn`, `kept-external`.
- **Takedown without serve-plane logic.** Parking moves objects under the
  reserved `_parked/{slug}/` prefix; serving learns through key existence,
  and the HTML cache revalidates via `Stat` after the TTL. Delete is the
  terminal op: guarded transition to `deleting` before any object is
  removed, both prefixes deleted idempotently, the row last. A crash
  mid-operation is healed by the lifecycle `Sweep` on admin/all boot.
  Deleted slugs' codes are never reused (counters only move forward).
- **One binary, mode-gated.** `SERVER_MODE=serve` boots from validated
  storage config only — no pool, no migrations, no bucket create, no sweep;
  `admin`/`all` own the boot duties. `db.Migrate` takes a Postgres advisory
  lock so concurrent admin replicas migrate safely.

## Deployment

One DNS name with a TLS cert, fronted by a CDN whose path routing is the
URL contract in both single-instance and split setups: `/p/*` and `/a/*`
are served by the **bucket directly** (the edge rewrites `/p/{slug}/…` to
bucket key `{slug}/…`, sets `X-Content-Type-Options: nosniff`, gives entry
HTML a short TTL with ETag revalidation, and assets the immutable headers
the app set); `/` and `/api/*` go to the Go service.

Non-negotiable at the edge:

- Missing keys map to **404** — S3's REST endpoint returns 403 on some code
  paths; translate both.
- After parking or deleting a page, purge `/p/{slug}/*` and `/a/{slug}/*`
  for edge-level removal.
- The serve instance gets **read-only storage credentials** and sits on the
  public tier; the admin instance (upload, ingest, lifecycle ops) sits on
  the internal network/VPN.

The two-instance split is opt-in and purely orchestration — the same image
runs twice, gated by `SERVER_MODE`. Adopt by deploying `all` everywhere
first, then flipping the edge's `/p/*`, `/a/*` origins to the serve
instance; rollback is a mode flip back to `all`.

## Build and test

Before declaring done: `gofmt -l .` empty, `go vet ./...` and
`go vet -tags=integration ./...` clean, `go test ./...` green, and — when
the change touches runtime behavior — `go test -tags=integration ./...`
green (real MinIO + Postgres via testcontainers).

Testing rules: unit tests run against the `mem` driver and fixture packs on
disk; integration tests use real containers. Mock the external asset
fetcher's HTTP and nothing else — never our own interfaces. Tests are
table-driven and live next to the code (e2e tests in `internal/e2e`).

## Configuration

Every variable, default, and validation rule lives in `internal/config`
(base table in [README.md](README.md)). The one non-obvious knob:
`HTML_CACHE_TTL` is also the window within which a parked page stops
serving.

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

## Working process

Plan first, discuss, then edit — and only what was asked. No commits
unless explicitly requested. When in doubt, ask.
