## Context

Today a page enters via `POST /api/pages` multipart (`file`): content-sniffed
into a zip pack or single HTML, then the ingest pipeline (scan → classify →
bake → rewrite) runs, storage puts objects, Postgres persists. Two facts shape
this design:

1. **The pipeline already fetches from the web** — `internal/ingest/fetch.go`
   bakes external assets with per-request timeout, budget, concurrency, and
   size cap — but with **no SSRF guard**: no private-range blocking, no
   redirect policy, and it runs on the admin plane (internal network) while
   baked objects are publicly readable at `/a/{slug}/external/…`. An uploaded
   page referencing `http://10.0.0.5/…` exfiltrates today. Import-by-URL makes
   a server-side fetch a first-class user input, which forces the guard to
   exist.
2. **Relative refs in a single-HTML upload are skipped** (`planSkip`,
   `pipeline.go` "broken local ref — leave untouched") and never appear in the
   manifest. A fetched page has a base URL, which turns those refs into
   resolvable, classifiable, manifest-visible references.

The manifest is the completeness report: `local`/`baked`/`kept-cdn` mean the
page is confident; `kept-external` means it is not.

## Goals / Non-Goals

**Goals:**
- Import a page by URL through the existing upload endpoint and pipeline.
- All web-fetching concerns (SSRF guard, caps, sniffing, charset) isolated in
  one package with no dependencies on page/storage concepts.
- Imports are complete-or-failed: strict gate on the import path only.
- Asset baking shares the guarded transport (closes the existing hole).
- Relative references resolve and bake; no new config knobs; no schema change.

**Non-Goals:**
- Headless/JS rendering, crawling, cookies/auth fetches (see proposal).
- Lenient import mode.
- Page provenance metadata (a `source_url` column) — considered, cut until a
  scenario needs it.

## Decisions

**D1 — Import enters through `POST /api/pages`, `url` field XOR `file`.**
The back half (auth, slug, ingest, persistence, `201` shape) is identical;
only the source of entry bytes differs, which is the same kind of variation as
the existing zip-vs-HTML content route. Neither field → `400` (existing
behavior); both → `422`.
*Alternatives considered:* separate `POST /api/import` endpoint — rejected:
duplicates the entire flow for no reader benefit.

**D2 — New package `internal/fetch`, bottom of the dependency graph.** It owns
the guarded transport and the entry fetcher; its contract is
`Entry{Data []byte, FinalURL *url.URL}` plus typed errors. It knows HTTP and
nothing about pages, slugs, or storage. `FinalURL` is part of the contract
because redirects make it a fact of fetching. The upload handler calls it for
the `url` branch and feeds the result into the existing single-HTML path.
*Alternatives considered:* inline shim in the upload handler (smears
network/security concerns into handler code); a file inside `ingest` (works,
but `ingest` is transforms-of-a-pack and the guard then has no natural shared
home).

**D3 — SSRF guard at the transport level, shared by entry fetch and asset
baking.** In `internal/fetch`: scheme allowlist (http/https); resolve the host
and reject loopback, RFC1918, link-local (incl. `169.254.169.254`), ULA,
CGNAT, `0.0.0.0`, multicast; a `CheckRedirect` that re-validates every hop;
dial pins the validated IP to close the DNS-rebinding TOCTOU. `ingest.Fetcher`
adopts this transport. Test seam: the guard is injectable so tests can use a
permissive variant — `httptest` servers live on loopback, which the guard
blocks (this is the one place the "mock only external HTTP" testing rule needs
a seam, and it is an external-HTTP seam, not one of ours).
*Alternatives considered:* guard only the entry fetcher — rejected: asset
baking has the same exfiltration vector today and is the bigger hole.

**D4 — The pipeline gains an optional base URL; relative refs resolve against
it and follow the existing classify path.** Resolution order in
`planIdentity`: skip-class (`data:`, `#`, `mailto:`, …) → absolute →
pack-local (uploads; never coexists with a base in practice) → base-absolute →
skip. A `<base href>` element overrides the source URL as resolution base when
present, matching browser semantics. The same mechanism fixes the latent gap
where relative refs inside a baked external stylesheet resolved against a
path-only base dir and silently 404'd. Without D4 the strict gate is blind:
`planSkip` refs are manifest-invisible, so an import could "pass" with broken
images — D4 is a correctness requirement of D5, not a nicety.
*Alternatives considered:* fetch-and-feed without base resolution — rejected:
only self-contained pages import well, and the gate cannot see the breakage;
fetcher pre-resolves/rewrites refs itself — rejected: duplicates the pipeline's
one job.

**D5 — Strict manifest gate on the import path, between `Process` and
`putObjects`.** `Process` is pure computation; `putObjects` is where side
effects start. If the request came from a `url` import and the manifest
contains any `kept-external` row, the handler returns `422 import_incomplete`
with `unresolved[]` (source URL + fetch-failure reason) and guidance to save
the page in the browser and upload instead; nothing is stored and no page row
is persisted. Uploads stay best-effort — the asymmetry is principled: uploads
are best-effort because the user has the bytes in hand; imports are
all-or-nothing because the user has nothing invested. The gate necessarily
runs after slug allocation (ingest is slug-dependent), so a failed import burns
a slug code — identical to today's ingest-failure path; counters only move
forward. Retaining the fetch error on kept plans (small change in
`ingest/pipeline.go`) supplies the `reason` field.
*Alternatives considered:* strict flag inside the pipeline — rejected: policy
does not belong in the machinery; pre-gate before ingest — impossible, the
manifest only exists after processing.

**D6 — Entry-fetch failure mapping reuses the API's existing conventions.**
Malformed URL or blocked range → `422`; unreachable, timeout, or any
non-200 source response (the source did not serve the page) → `502` (honest
"upstream's fault"); response is not HTML → `415` (the handler already `415`s
non-HTML bodies); response over `UPLOAD_MAX_RAW_BYTES` → `413`. All before
any storage occurs.
*Alternatives considered:* collapsing everything into `422` — rejected: hides
whether the user or the source site should change something.

**D7 — Caps are reused, not added.** Entry fetch: `FETCH_TIMEOUT` per request,
`UPLOAD_MAX_RAW_BYTES` size cap; assets keep `ASSET_MAX_BYTES`/budget/
concurrency. No new configuration. *(Serves the bounded-import requirement
without new ops surface — no-knob principle.)*
*Alternatives considered:* dedicated `IMPORT_*` knobs — rejected: no scenario
distinguishes them yet.

**D8 — Charset normalization to UTF-8 happens in the fetcher** via
`golang.org/x/net/html/charset` (plus `x/text`, both already direct/indirect
dependencies — no new module). Uploads sidestep encoding concerns; imported
pages will not. *(Serves correct rendering of non-UTF-8 sources; it is a
property of the transfer, not of the transform.)*
*Alternatives considered:* normalize in the pipeline — rejected: wrong layer,
and `html.Parse` assumes UTF-8 already.

## Risks / Trade-offs

- [JS-assembled pages import confidently wrong — every detected ref resolves,
  invisible ones are simply absent] → stated boundary in spec, failure-message
  copy, and UI; save-as+upload remains the escape hatch. No static scanner can
  close this.
- [Bot-blocked sources (403, challenge pages) either fail or import a
  challenge page] → accepted; a challenge page passes HTML sniffing but the
  result is visibly wrong, and the strict gate still catches asset failures.
- [Guard breaks existing fetch tests (httptest on loopback)] → injectable
  permissive guard in tests; no production knob.
- [Custom pinned-dial transport is subtle code] → contained in one package,
  table-tested (each guard rule a case); no behavior change for plain public
  fetches.
- [Failed import burns a slug code] → identical to the existing ingest-failure
  path; counters only move forward by design.
- [Strict gate makes imports feel fragile on asset-heavy sources] → the `422`
  body lists exactly which URLs failed and why, so the user can judge
  (e.g. hotlink-blocking) instead of debugging a broken page.
