## Context

The admin plane has upload + `GET /api/pages/{slug}` + park/unpark, but no
list endpoint and no way to remove a page. The UI is a 74-line upload form.
The `pages` table already carries everything a list view needs
(slug, identifier, code, status, asset_count, total_bytes, created_at).
Lifecycle (`internal/lifecycle`) owns a guarded-transition state machine on
`pages.status` (`live`/`parking`/`parked`/`unparking`), per-slug in-process
locks, copy-before-delete object moves, and a boot `Sweep` that resumes
interrupted toggles. Status values are constrained by a CHECK added in a
prior migration.

Constraints that shape this design: assets are immutable under `{slug}/`
with `max-age=31536000, immutable` — same-slug content replacement would be
served stale by every cache for up to a year, so "update" must mean
lifecycle-only; serving is key-existence arithmetic with no DB; the repo bans
structure without a direct consumer.

## Goals / Non-Goals

**Goals:**

- `GET /api/pages`: paginated list with status filter, bearer-authed.
- `DELETE /api/pages/{slug}`: safe hard delete of objects + rows, serialized
  against in-flight toggles, crash-resumable by the existing sweep.
- Management UI at `/`: list / detail / upload views, single hand-written
  HTML file, no build step.

**Non-Goals:**

- In-place content update or versioning (see proposal Non-goals).
- Sessions, login, CSRF; search; audit log; bulk ops; CDN-purge automation.

## Decisions

**D1 — Delete is a terminal lifecycle operation, implemented on
`lifecycle.Service`.** `Delete(ctx, slug)` takes the per-slug lock, performs
a guarded transition `live|parked → deleting` (0 rows ⇒ 404 unknown, or 409
if the current status is a transient `parking`/`unparking`), then deletes
objects, then removes the DB row (assets cascade). Alternatives considered:
a new `internal/pagemanage` package (rejected: it would duplicate the
lock/transition machinery or import-cycle around it; the repo bans structure
without a consumer); an unlocked read-then-delete (rejected: races a
concurrent park — objects would be re-materialized at `_parked/` after
deletion by the toggle's copy step, leaking storage).

**D2 — A `deleting` status records intent before any object is removed, and
`Sweep` resumes it.** Extends the status CHECK
(`live, parking, parked, unparking, deleting`) via one new migration. The
sweep treats `deleting` like any other mid-flight state: re-delete both
`{slug}/` and `_parked/{slug}/` (both idempotent), then delete the row.
Alternatives considered: no new status, delete only in settled states under
the process lock (rejected: a crash between object deletion and row deletion
leaves a phantom "live" row with no objects — invisible to the sweep, wrong
in the list UI forever); a separate tombstone table (rejected: second source
of truth for state the status column already owns).

**D3 — Delete order: status first, then both prefixes, then the row.**
`_parked/{slug}/` and `{slug}/` are deleted unconditionally and
idempotently — the page can only occupy one of them, and "delete both" is
simpler and crash-safe. Objects removed before the row: a crash leaves a
`deleting` row (visible, honest) rather than a live row with missing
objects. Serving stops at first-prefix-deletion plus the usual
revalidation window; no serve-plane change. Alternative considered:
delete row first (rejected: row gone while objects still serve ⇒
unmanageable orphan storage).

**D4 — List lives on `upload.Handler` alongside `Get`, backed by
`db.ListPages`.** One query with `LIMIT`/`OFFSET` plus a `COUNT(*)` with the
same filter, ordered `created_at DESC, slug DESC`. Sizes hardcoded in code
(default 50, cap 500) — no config knob, no scenario needs operator tuning.
Alternatives considered: a new handler package (rejected: `upload.Handler`
already owns `GET /api/pages/{slug}` with identical auth/JSON plumbing; a
package move is churn with no behavior change); cursor pagination (rejected:
admin-scale page counts, offset links are trivially renderable in the UI).

**D5 — Mounting follows the existing plane split.** `GET /api/pages` and
`DELETE /api/pages/{slug}` mount inside the existing `api()` block
(admin/all only); serve mode keeps 404ing all of `/api/*`. No
deployment-modes delta: the path contract (`/api/*` on the admin plane) is
unchanged. Alternative considered: separate mount points per method
(rejected: existing pattern already covers it).

**D6 — UI: one static HTML file grows into a hash-routed SPA.** Views
`#/` (list), `#/p/{slug}` (detail + actions), `#/upload` (the existing form
verbatim in behavior). Vanilla JS, `fetch` with the stored bearer token,
native `confirm()` before delete, status badges, ‹prev/next› pagination off
the list response's `total`. No framework, no build step, embedded as today
via `static/index.html`. Alternatives considered: Go `html/template`
server-rendered pages (rejected: every action becomes a full round-trip and
the token handoff gets clunkier for zero benefit at this scale); htmx or a
JS framework (rejected: dependency without a consumer — the repo principle).

**D7 — Auth is the existing bearer pattern, unchanged.** Same constant-time
compare, same `AUTH_TOKEN`, UI keeps pasting the token into `localStorage`.
Both handlers already duplicate this helper; the new list/delete handlers
follow suit rather than introducing a shared auth abstraction for two call
sites (repo: no indirection without multiple real consumers... and even
then, only the storage seam is sanctioned).

## Risks / Trade-offs

- [Rollback mid-delete leaves a `deleting` row the old binary's sweep
  ignores] → Documented: old sweep only resumes `parking`/`unparking`. Ops
  remedy is one SQL `DELETE` + `DeletePrefix`; acceptable for an internal
  tool, called out in the migration plan.
- [`DELETE` is destructive and bearer-token-only] → Same trust model as
  park today (one shared admin token on a private tier). UI adds a typed
  confirmation; API adds the 409 guard against racing toggles.
- [List `COUNT(*)` on every page view] → Page counts are hundreds, not
  millions; indexed scan is fine. Revisit only if the tool outgrows its
  purpose.
- [UI grows past one file's comfort] → Accepted trade-off; if the file
  becomes genuinely unwieldy, that is the first day htmx earns its keep —
  not before.

## Migration Plan

1. Land migration (status CHECK extension) — backward compatible: old code
   never writes `deleting`.
2. Deploy new binary (single image, `SERVER_MODE` unchanged). No storage
   format or URL changes.
3. Rollback = previous image. Known residue: see the risk above for a
   `deleting` row caught mid-flight at rollback time.

## Open Questions

None — scope and decisions settled during exploration.
