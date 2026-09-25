# Proposal: add-page-park-toggle

## Why

Pages are immutable and append-only: once uploaded, a page is public forever at its URL, and the only "removal" is hard-deleting objects — which is irreversible, loses the URL (the slug counter never rewinds), and leaves caches serving stale copies. We need takedown as a **toggle**: hide a page and bring it back later at the same URL.

## What Changes

- New admin endpoints to **park** (hide) and **unpark** (restore) a page, bearer-token authenticated, idempotent.
- Parking moves a page's objects under a reserved `_parked/{slug}/` bucket prefix and back; **the serve plane is untouched** — it keeps doing URL→key arithmetic and 404s parked pages naturally by key absence.
- Durable lifecycle state machine (`live / parking / parked / unparking`) recorded in one new `pages.status` column; crashes mid-toggle recover by idempotent retry and a boot-time sweep.
- **Cache-header split**: entry HTML becomes revalidatable (short `max-age` + ETag); assets keep `immutable, max-age=31536000`. The in-memory entry-HTML cache gains TTL-bounded revalidation via `Stat`, so parked pages stop being served within a bounded window at both CDN and origin.
- Storage seam grows by one vendor-neutral operation, `Copy` (server-side rename doesn't exist in S3; park = copy+delete sequenced around the entry HTML "door").
- Manifest API response gains a `status` field.

## Capabilities

### New Capabilities

- `page-lifecycle`: Park/unpark toggle — API, bucket-prefix parking, state machine durability, same-URL restore with stable ETags, status visibility.

### Modified Capabilities

- `object-storage`: The storage seam grows from four to five operations — `Copy` joins `Put`/`Get`/`Stat`/`DeletePrefix`, implemented by both drivers; the vendor-neutrality rule itself is unchanged.
- `page-serving`: Cache headers are split (entry HTML revalidatable, assets stay immutable), and the in-memory entry cache gains TTL-bounded revalidation so parked/removed pages stop being served.

## Impact

- `internal/db/migrations`: new `0002` migration (`pages.status`, default `live`).
- `internal/storage`: `Copy` on the seam + `s3compat`, `mem` drivers + conformance suite.
- `internal/serve`: header split, entry-cache revalidation.
- Admin API: toggle endpoints + manifest `status` field; migration runs at admin boot as today.
- Slug scheme: **no change** — `_parked` cannot collide because sanitization excludes underscores.

## Non-goals

- No serve/admin **deployment** split (separate future change; this feature touches only the admin plane).
- No CDN purge API integration — purge on park is a documented ops step; no edge-KV kill switch.
- No hard-delete endpoint, no page history/audit table, no per-page ACL or expiry.
