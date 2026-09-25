# Design: add-page-park-toggle

## Context

Pages are immutable objects under `{slug}/` keys; the serve plane is DB-free URL→key arithmetic; Postgres holds write-side bookkeeping only (`pages`, `assets`, `counters` — see `internal/db/migrations/0001_init.sql`). The storage seam is exactly four ops (`Put`, `Get`, `Stat`, `DeletePrefix`). Serving caches entry HTML in memory forever (immutability made that safe). There is no way today to hide a page; hard delete would be irreversible, would lose the URL (slug counters never rewind), and would leave CDN/browser caches serving stale bytes for up to a year.

## Goals / Non-Goals

**Goals:**
- Takedown as a **toggle**: hide a page, restore it later at the **same URL** with identical bytes.
- Serve plane learns about parking purely through key absence — no flag checks, no DB reads, no new route logic.
- Bounded staleness: a parked page stops being served at origin (and at a purged CDN) within a known window.
- Crash-safe: any interruption mid-toggle is recoverable by idempotent retry.

**Non-Goals:**
- Serve/admin deployment split (separate future change).
- CDN purge API integration (ops runbook step; vendor still undecided).
- Hard delete, page history/audit table, per-page ACL, expiry, edge-KV kill switch.

## Decisions

### D1: Park = move objects to a `_parked/{slug}/` prefix; the bucket is the toggle

Park copies `{slug}/*` → `_parked/{slug}/*` then deletes the originals; unpark reverses. Serveability hangs on key existence, so the serve plane needs **zero changes** and there is no second source of truth to drift.

- *Serves:* `page-lifecycle` (parked pages 404 naturally).
- **Alternatives considered:** DB flag checked at serve time (re-couples serve to Postgres — the split this architecture avoids); `.off` marker object per request (second hot-path lookup or a marker-set cache → invalidation protocol); edge-KV blocklist (third system holding truth; deferred as bolt-on if instant kill is ever required).

### D2: Copy-before-delete in both directions — failures are fail-visible

S3 has no atomic rename (keys are flat strings; "folders" are prefix conventions). The move is therefore a copy sweep followed by a delete sweep, ordered so every crash point is safe:

- **Park:** `Copy("{slug}/", "_parked/{slug}/")` → `DeletePrefix("{slug}/")` — the parked copy is complete before any original is deleted; a crash leaves the page still visible (retry converges).
- **Unpark:** `Copy("_parked/{slug}/", "{slug}/")` → `DeletePrefix("_parked/{slug}/")` — a crash leaves the page visible with possibly a few missing assets for seconds (retry converges).

The failure bias is "not hidden yet", never "content destroyed". The entry HTML acts as an emergent door: it reappears when its copy lands during unpark (assets may trail by seconds) and disappears with the live-prefix delete during park — no special sequencing needed beyond the copy-before-delete rule.

- **Alternatives considered:** delete-first ordering (crash loses the only copy — data loss); entry-first/entry-last single-key sequencing (needs a single-object Copy + key enumeration → forces a banned `List` onto the seam; superseded by prefix `Copy`); relying on bulk atomicity (does not exist in S3/MinIO/R2).

### D3: Lifecycle state machine in one DB column

`pages.status` ∈ `live / parking / parked / unparking` (migration `0002`, default `live`, CHECK constraint). Status is recorded **before** any bucket mutation and finalized after; boot re-sweeps rows stuck in `parking`/`unparking` by resuming the idempotent move. Transitions guarded by `UPDATE … WHERE status = <expected>` so concurrent toggles serialize. DB stores **intent**, never object location — location is derivable from status and remains the bucket's truth.

- *Serves:* crash recovery + admin-plane visibility. No new tables, no counters touched (park/unpark consumes no slugs).
- **Alternatives considered:** no DB state (nothing to resume or audit); append-only `page_events` history table (YAGNI until actor identities exist — revisit when auth users arrive).

### D4: `Copy` joins the storage seam (fifth vendor-neutral op, prefix-based)

Park needs server-side copy, and the engine must not enumerate keys (that would drag a banned `List` onto the seam). So `Copy` is **prefix-based** — `Copy(srcPrefix, dstPrefix)` copies every object under the source prefix to the destination, symmetric with `DeletePrefix`, which already hides its internal listing the same way. Every S3-compatible vendor has CopyObject; all page objects are ≤10 MiB (`ASSET_MAX_BYTES`), so no multipart copy is needed and ETags survive round-trips. Both drivers implement it; `storagetest` conformance covers byte/content-type/ETag preservation and destination-overwrite semantics. The seam rule stays "no growth unless a requirement demands it" — this is that requirement.

- **Alternatives considered:** single-object `Copy` + engine-side enumeration (forces `List` onto the seam — six ops and a new primitive class); Get→Put streaming through the admin server (keeps four ops but flows bytes through the box and makes admin a data plane); vendor-specific rename APIs (do not exist portably).

### D5: Cache-header split + TTL-bounded entry-cache revalidation

Assets (≈all the bytes) keep `Cache-Control: public, max-age=31536000, immutable`. Entry HTML switches to short `max-age` + `must-revalidate` + ETag. The in-memory entry cache, which today never expires, revalidates each entry against storage via `Stat` after a TTL (`HTML_CACHE_TTL`, default 60s — knob exists to serve the bounded-staleness requirement, not speculatively). On revalidation miss (parked), the entry is dropped and the request 404s. Restores are free: content is unchanged, so cached copies revalidate to 304.

- *Serves:* `page-lifecycle` bounded-staleness requirement at origin, mirroring the CDN-side header split.
- **Alternatives considered:** keep never-expiring cache + explicit eviction messaging from admin (new inter-service protocol for one problem); no entry cache (regresses the D14 serving design).

### D6: Synchronous, idempotent toggle API

`POST /api/pages/{slug}/park` and `.../unpark`, bearer-authenticated, run the bounded move inline (a few seconds at caps — same work profile as synchronous ingest) and return the resulting status. Re-POST during `parking`/`unparking` resumes rather than errors. Unknown slug → 404; unauthenticated → 401.

- **Alternatives considered:** async job + status polling (a queue/worker subsystem for a bounded, rare admin operation); returning 409 on concurrent toggle (unnecessary — D3's guarded UPDATE makes re-POST safe).

## Risks / Trade-offs

- [Crash mid-move leaves objects split across prefixes] → D2 door ordering + D3 intent-before-mutation + idempotent retry/boot sweep; worst case is a visible half-state that converges.
- [CDN/browser caches may serve a parked page until purge/TTL] → entry HTML bounded by header split + revalidation TTL; assets are an accepted ops purge step (non-goal: vendor integration). Browser-held copies are unrecoverable — inherent to caching, accepted.
- [Extra `Stat` load from revalidation] → one Stat per cached slug per TTL per instance at 60s default; negligible.
- [Vendor CopyObject quirks (ETag/metadata)] → conformance scenario in `storagetest` runs against real MinIO in integration.
- [Readers mid-toggle see transient 404s on assets behind an open door] → unavoidable without transactions; assets load within seconds of the door reopening (unpark copies assets before the door).

## Migration Plan

1. Ship `0002` migration (additive, default `live`; no backfill) and the new binary together — single deployment today, migration runs at boot as now.
2. Rollback: redeploy previous binary (new column is inert to it); `ALTER TABLE pages DROP COLUMN status` optional cleanup.
3. Ops runbook addition: after parking, purge CDN URLs for `/p/{slug}/*` and `/a/{slug}/*` if edge removal is required.

## Open Questions

- Exact entry-HTML `max-age` value (default proposal: 60s, matching the revalidation TTL) — tunable in review.
