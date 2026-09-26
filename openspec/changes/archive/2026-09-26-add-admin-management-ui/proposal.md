## Why

The web UI at `/` can only upload. Once a page exists there is no way to see
it — no list, no status, no manifest — and takedown (park/unpark) and cleanup
(delete) are curl-only. Managing published pages is the day-to-day job of this
tool; it deserves a real admin surface.

## What Changes

- New `GET /api/pages` list endpoint: bearer-authed, paginated
  (`limit`/`offset`), optional `status` filter, returns `total` plus page
  metadata (slug, identifier, code, status, asset_count, total_bytes,
  created_at, url).
- New `DELETE /api/pages/{slug}`: bearer-authed hard delete — refuses while a
  lifecycle transition is in flight (409), removes storage objects
  (`{slug}/` for live, `_parked/{slug}/` for parked) and the DB rows (assets
  cascade). Content is gone at origin; CDN purge stays an ops step, same as
  park.
- The single upload form at `GET /{$}` becomes a management UI (same
  hand-written HTML file, no build step): list view (table + pagination +
  status badges), detail view (metadata + manifest table + park/unpark +
  delete), upload view (the existing form, unchanged contract). Token entry
  and storage in `localStorage` as today.
- Update semantics stay lifecycle-only: park/unpark is the one mutable field.
  Content changes are delete + re-upload (new slug), preserving immutable
  assets under `{slug}/` and their year-long cache headers.

## Capabilities

### New Capabilities

- `page-management`: list and delete APIs over existing pages — pagination,
  status filter, delete safety (transition conflict), object removal for both
  live and parked prefixes.
- `admin-ui`: the management web UI at `/` — list, detail, and upload views,
  wired to the APIs with bearer auth from the browser.

### Modified Capabilities

- `page-upload`: the "Minimal upload UI" requirement is replaced — the page
  at `GET /{$}` is now the management UI, with upload as one view. The upload
  API contract itself is unchanged.

## Impact

- `internal/db`: new `ListPages` query; `DeletePage` (transactional row
  delete).
- `internal/upload`: list handler; `internal/pagemanage` (or upload pkg —
  design decides): delete handler calling `storage.DeletePrefix` + DB.
- `internal/serve`: mount `GET /api/pages` and `DELETE /api/pages/{slug}` on
  the admin/all planes (serve plane still 404s `/api/*` — no change).
- `internal/serve/static/index.html`: rewritten as the management UI.
- No schema migration needed (existing tables suffice); no new config.

## Non-goals

- No in-place content update/replace — immutable assets under `{slug}/` and
  `max-age=31536000, immutable` make same-slug replacement a cache-poisoning
  bug; content change = upload new slug + delete old.
- No sessions/login/CSRF — the token-paste model stays.
- No search beyond what the list endpoint returns; no audit log; no bulk
  operations; no JS framework or build step.
