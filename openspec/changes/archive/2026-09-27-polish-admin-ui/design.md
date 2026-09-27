## Context

The management UI is a single hand-written file, `internal/serve/static/index.html`,
embedded with `go:embed` and served at `/` on the admin/all planes
(`internal/serve/serve.go:24`). No build step, no framework, hash-routed SPA
over the existing JSON APIs. Exploration (rendered with a stubbed fetch, light
scheme, 1280px and 390px viewports) confirmed the gaps in the proposal. The
lifecycle APIs are synchronous (`internal/lifecycle/api.go`), so no polling is
needed; the upload 201 response already returns an asset status count map
(`internal/upload/upload.go:177`) that the UI ignores. The `admin-ui` spec
constrains the UI to one file, no build step, no framework — every decision
below stays inside that.

## Goals / Non-Goals

**Goals:**

- Make `kept-external` legible as the accepted risk it is, wherever ingest
  outcomes appear (detail manifest, upload result).
- Clear the token row out of prime position without weakening the 401 flow.
- Light-theme visual polish: consistent colors, hover/focus feedback, AA
  contrast, scannable timestamps.
- Keep the action column reachable at narrow widths.
- Stay a single hand-written file.

**Non-Goals:**

- Dark theme (pin light; operator runs light).
- Version grouping, slug search, upload progress (see proposal Non-goals).
- Any backend/API/schema change.

## Decisions

**D1 — Pin light scheme with explicit canvas.** Replace `color-scheme: light dark`
with `color-scheme: light` and set explicit `background`/`color` on `body`.
Alternatives: maintain a full dark palette (rejected: doubles the visual
test surface for a light-theme operator); leave `light dark` (rejected: on a
dark-canvas browser the page renders half-broken — dark background under
light-hardcoded badges — which was observed during exploration).

**D2 — A small palette as CSS custom properties.** Define the accent, status
(live/parked/transient/external), error, and muted colors as `:root` variables
and use them everywhere, replacing scattered hex and bare `gray`. Muted moves
to a value with ≥ 4.5:1 contrast on white. Alternatives: raw hex inline
(current; the inconsistency is part of the problem); an imported design-token
file (rejected: no build step, no dependencies).

**D3 — `kept-external` = amber badge plus one contextual footnote.** In the
manifest, style `kept-external` amber (same visual family as transient
lifecycle badges); when any asset is `kept-external`, render one line under
the manifest table: "N asset(s) still load from their original site and may
break or leak requests." `kept-cdn` gets a neutral badge; any asset with
`0 B` shows `—` instead. Alternatives: per-asset tooltip (hidden on touch,
more JS for less visibility); a page-level warning banner (overstates the
risk — the page still serves); a new API field (rejected: statuses are
already in the manifest response).

**D4 — Rename the manifest column to `Storage`.** The manifest hosts ingest
statuses (`baked/local/kept-cdn/kept-external`), a different state machine
from lifecycle `Status` on the list. Renaming removes the false sibling-hood.
Alternative: keep `Status` with distinct badge styling (rejected: the shared
header is itself the confusion).

**D5 — Token chip replaces the always-open row.** Render a small chip in the
header showing state ("Token set" / "No token"); clicking toggles the input
row; a 401 auto-expands the row with the existing error message. Token stays
in `localStorage`; the existing re-route-on-input behavior is unchanged.
Alternatives: keep the row always visible (rejected: it occupies the top of
every view while usually holding nothing); a modal prompt (heavier, and the
inline row already handles the re-entry flow).

**D6 — Relative timestamps with absolute fallback.** Created columns render
`Intl.RelativeTimeFormat` output ("just now", "1 day ago"); the full locale
datetime moves into the element's `title`. Both APIs are built-in — no
dependency. Alternatives: short absolute dates (ambiguous day/month and
timezone); keeping the full datetime (it scans poorly and the seconds are
noise).

**D7 — Upload drop zone.** Replace the bare `input[type=file]` with a styled
drop zone: click-to-browse (label wrapping the existing input, hidden
visually but kept for a11y) plus `dragover`/`drop` handlers accepting
`.html/.htm/.zip`. The chosen file's name and size display in the zone; the
submit path (FormData upload, error handling) is unchanged. Alternatives:
only restyle the native input (rejected: leaves the weakest control in the
primary flow); a JS upload library (rejected: no dependency without a
consumer).

**D8 — Upload success shows the ingest summary.** The 201 body's `assets`
map (`baked/local/kept_cdn/kept_external` counts) renders as a count line in
the existing success box, amber when `kept_external > 0`, with the existing
details link. The form is not navigated away from. Alternative: redirect to
the detail page (rejected: spec scenario requires the URL be displayed on
the form and the form state preserved; inline keeps multi-upload flow intact).

**D9 — Table overflow container.** Wrap both tables in
`div.tablewrap { overflow-x: auto; }` so the action column scrolls into view
instead of clipping at narrow widths. Alternative: responsive card layout
below a breakpoint (rejected: over-engineering for an admin tool; revisit if
mobile use becomes real).

**D10 — Interaction polish.** Row hover, button hover/active, `:focus-visible`
rings, `color: inherit` on the title link, one accent color for all links,
identifier hidden when identical to slug, empty-state text becomes a link to
`#/upload`. Alternatives: leave to UA defaults (current; no feedback until
click and default blue/purple links read as unstyled, not intentional).

## Risks / Trade-offs

- [Pinned light scheme makes dark-OS users see white] → Accepted: the
  operator runs light; the alternative (half-broken auto-dark) is worse.
  Revisit if dark use appears.
- [Relative timestamps go stale on a long-lived view] → Times are computed
  at render; every mutation re-renders, and the absolute value stays in
  `title`.
- [Drop-zone edge cases (folders, wrong MIME)] → The zone filters by
  extension client-side only as a hint; the server remains the validator and
  its errors already render in the result box.
- [e2e tests assert on rendered UI text] → The e2e suite exercises APIs with
  its own fixtures and does not parse this file; browser QA is the
  verification path for the visual items.

## Open Questions

(none — scope deliberately excludes the items that would need product
decisions: version history, search, dark theme)
