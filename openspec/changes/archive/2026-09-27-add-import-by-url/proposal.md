## Why

Creating a page requires the user to first obtain the HTML themselves (browser
save-as, SingleFile, curl) and then upload it. The system already fetches from
the web during ingest (asset baking), so it can fetch the entry document too:
the user gives a URL, the server does the rest. Convenience is the driver;
fidelity is the payoff — a fetched page has a base URL, so its relative asset
references can be resolved and baked instead of left dangling.

## What Changes

- New `POST /api/pages` input: a `url` form field (XOR the existing `file`),
  same auth, slug assignment, ingest, persistence, and `201` response shape.
- New package `internal/fetch`: a full-featured, isolated web fetcher —
  SSRF-guarded transport (scheme allowlist, private-range blocking, IP pinning
  at dial, per-hop redirect re-validation), bounded GET, HTML content sniffing,
  charset normalization to UTF-8. Contract: entry URL → `{HTML bytes, final
  URL}`. It knows nothing about pages or storage.
- Ingest pipeline accepts an optional base URL: relative references resolve
  against it and follow the existing classify/bake/keep path (import-by-url D4).
  This also fixes latent broken relative refs inside baked external CSS.
- Strict import gate: for URL imports only, any `kept-external` asset in the
  manifest fails the request with `422 import_incomplete`, listing unresolved
  assets and advising save-as + upload instead. Uploads stay best-effort.
- `ingest.Fetcher` (asset baking) adopts the fetch package's guarded transport,
  closing an existing SSRF hole where uploaded HTML could bake internal-network
  URLs into publicly readable objects.
- Failure mapping for the entry fetch: malformed/blocked URL → `422`, source
  unreachable/5xx/timeout → `502`, non-HTML response → `415`, over size cap →
  `413`.

## Capabilities

### New Capabilities
- `web-fetch`: guarded server-side web fetching — SSRF guard semantics, bounded
  fetch with size/timeout caps, HTML validation, charset normalization.

### Modified Capabilities
- `page-upload`: URL import as an alternative upload source; entry-fetch
  failure semantics; strict completeness gate on the import path.
- `ingest-pipeline`: base-URL reference resolution for relative refs; relative
  refs inside baked external CSS resolved; kept-external rows carry the fetch
  failure reason.
- `admin-ui`: upload view accepts a source URL as an alternative to file
  selection and surfaces import failure guidance.

## Impact

- New `internal/fetch` package (no internal dependencies below it).
- `internal/ingest`: plan resolution, CSS base dirs, fetcher transport, error
  retention on kept plans.
- `internal/upload`: URL branch, manifest gate, error→status mapping.
- Admin UI upload form: URL input alongside file selection.
- `internal/config`: no new knobs — entry fetch reuses `FETCH_TIMEOUT`,
  `UPLOAD_MAX_RAW_BYTES`; assets keep existing caps.
- Admin UI upload form gains a URL input.
- `go.mod` unchanged (`x/net`, `x/text` already present).
- Non-goal: strictness covers *detected* references; JS-assembled pages may
  still import incomplete (documented boundary).

## Non-goals

- No headless-browser/JS rendering — import gets what curl gets.
- No crawling or multi-page mirroring — one document per import.
- No asset pre-fetching in the fetcher — baking stays in the pipeline.
- No cookies/auth/session handling for source fetches.
- No new configuration knobs; no lenient-import mode (add later with a scenario).
