# Proposal: one-surface-upload-ui

## Why

The upload view presents URL import and file upload as two independent,
always-active fields with no visible relationship. Their exclusivity is
undiscoverable until the user hits the "Provide a URL or a file, not both"
error, the explanation of what each option does lives in an intro paragraph
nobody re-reads, and the field order buries the actual decision (source)
behind an optional field. A validated prototype
(`prototype-upload.html`, variant A — "stacked card") resolved the design.

## What Changes

- The upload view becomes a single card: a drop layer (drag-and-drop or
  click-to-browse) on top, a divider, and a paste-a-link input as the
  card's footer. The card always shows exactly the one source that will be
  published, as a green summary chip with a clear button.
- **Last-action-wins** replaces the "not both" error: attaching a file
  replaces a typed URL and vice versa. The server's `url XOR file`
  validation (422) is unchanged and stays as the backstop; the UI simply
  can no longer produce "both".
- The heading becomes "Publish new page"; the intro paragraph and the
  "we fetch it and bake its assets" hint are removed.
- The identifier becomes mandatory in the UI (FE-only validation; the API
  still accepts an omitted identifier and defaults to `page`) and shows a
  live preview of the final URL (`/p/{sanitized}-1`), mirroring the slug
  sanitizer and reserved-word check from `internal/slug`.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `admin-ui`: The upload view requirements change — drop zone and URL input
  are restructured into one source card with last-action-wins selection; a
  new requirement covers the mandatory identifier with live URL preview;
  the upload-result and import-error requirements carry over unchanged.

## Impact

- `internal/serve/static/index.html`: the `renderUpload` view and its CSS.
  No Go code changes — the POST /api/pages contract, validation, and
  ingest pipeline are untouched.
- `README.md` screenshots show the old upload view; refresh after landing.

## Non-goals

- No server-side change to identifier handling (stays optional with the
  `page` default); no new API endpoint for querying the next slug counter.
- No segmented tabs, mutual disabling, or separate modes — one card, one
  visible source.
- No auto-publish on drop, no page-title preview, no "publish another"
  loop, no tooltips — the card copy carries the explanation.
- No dark theme or layout changes outside the upload view.
