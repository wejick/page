## Why

The management UI undersells its own data and reads as unattended. The one
accepted-risk signal — `kept-external` assets, whose visibility is the entire
rationale for that compromise — renders as a generic badge identical to the
harmless `local`, with a misleading `0 B` size. Around it: the page title and
links are unstyled browser defaults (blue, purple after visiting), there are
no hover/focus states, the API token row permanently occupies the top of the
page even when a valid token is stored, and at phone width the table's action
buttons are pushed off-screen entirely.

## What Changes

All client-side in the single `index.html`; no API, storage, or config change
(the upload 201 response already returns asset status counts that the UI
ignores).

- Manifest presentation: style `kept-external` as a warning with a one-line
  explanation, style `kept-cdn` as neutral, show `—` instead of `0 B` for
  unstored assets, rename the manifest column `Status` → `Storage` (it hosts
  ingest statuses, not lifecycle ones).
- Upload success result shows the ingest summary the API already returns
  (N baked, M kept external, …).
- Token entry collapses into a compact status chip; it expands on click and
  automatically on 401.
- Light-theme polish: pinned light color scheme with explicit background,
  title/link colors no longer browser-default, hover and `:focus-visible`
  states, muted text at WCAG AA contrast, relative creation times with the
  full date as tooltip, identifier hidden when identical to slug, empty list
  state offers a real Upload CTA.
- Upload file input becomes a styled drop zone (click-to-browse and
  drag-and-drop).
- Table wrapped in an `overflow-x` container so actions stay reachable at
  narrow widths.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `admin-ui`: manifest/ingest-status presentation, token entry affordance,
  upload success summary, drop-zone upload, visual polish and narrow-viewport
  behavior become specified requirements.

## Impact

- Code: `internal/serve/static/index.html` only (CSS and JS render functions).
- Tests: management e2e tests in `internal/e2e` assert on rendered text and
  may need updated expectations; no API or backend tests change.

## Non-goals

- Dark theme. The operator runs light; we pin light rather than maintain
  two palettes.
- Version history / grouping by identifier, slug search: product decisions
  for a separate change.
- Upload progress reporting (XHR swap): no scenario at current pack sizes.
- Polling for transient statuses: lifecycle APIs are synchronous; transients
  appear only after a crash, and Sweep heals them on boot.
