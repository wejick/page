# ingest-pipeline Specification

## Purpose
Bake-at-upload pipeline: reference scanning, keep-external classification, bounded baking, URL rewriting, per-asset manifest.
## Requirements
### Requirement: Entry point detection
For zip uploads, the system SHALL use `index.html` at the zip root as the entry point, or fall back to the shallowest-depth `.html` entry; a zip containing no `.html` entry MUST be rejected with `422`.

#### Scenario: Index at root
- **WHEN** a zip contains `index.html` at its root
- **THEN** `index.html` becomes the page entry point

#### Scenario: No index file
- **WHEN** a zip has no root `index.html` but contains `page.html` at depth 1 and `deep/nested/x.html`
- **THEN** `page.html` becomes the entry point

#### Scenario: No HTML at all
- **WHEN** a zip contains only images
- **THEN** the API returns `422` and stores nothing

### Requirement: Entry normalization
The system SHALL store the detected entry HTML as `{slug}/index.html` in the bucket, regardless of its source filename or location in the pack, so that page URLs resolve to storage keys by arithmetic alone.

#### Scenario: Non-index entry renamed
- **WHEN** the detected entry point of a pack is `page.html`
- **THEN** it is stored at key `{slug}/index.html` and the page resolves at `/p/{slug}/`

#### Scenario: Nested entry renamed
- **WHEN** the detected entry point is `docs/deep/page.html`
- **THEN** it is stored at key `{slug}/index.html`, not under its original path

### Requirement: Reference scanning
The system SHALL scan entry HTML and all reachable CSS for asset references: `src`, `href`, `srcset`, `poster`, inline `style` attributes, `<style>` blocks, SVG `<image>`/`<use>`, CSS `url()`, `@import`, and `@font-face` sources — including references inside fetched CSS files (recursive).

#### Scenario: Srcset with multiple candidates
- **WHEN** an `<img>` has a `srcset` with three URLs
- **THEN** all three URLs are processed as individual asset references

#### Scenario: CSS import chain
- **WHEN** entry HTML links `a.css`, which `@import`s `b.css`, which references `font.woff2` via `url()`
- **THEN** all three files are processed and their internal references rewritten

### Requirement: External asset classification
For each external reference the system SHALL classify in order: refs with signed/expiring query parameters SHALL be baked; refs to hosts on the configured keep-external allowlist SHALL be kept external and recorded as `kept-cdn`; all remaining external refs SHALL be baked. The allowlist MUST live in configuration (categorized), not code.

#### Scenario: Google Fonts kept external
- **WHEN** HTML references `https://fonts.googleapis.com/css2?family=Inter`
- **THEN** the ref is kept as-is and recorded as `kept-cdn` (its gstatic children are never fetched)

#### Scenario: Known JS CDN kept external
- **WHEN** HTML references `https://cdn.jsdelivr.net/npm/lib.js`
- **THEN** the ref is kept as-is and recorded as `kept-cdn`

#### Scenario: Unknown host baked
- **WHEN** HTML references an image on a host not on the allowlist
- **THEN** the asset is fetched and stored in the bucket

#### Scenario: Signed URL baked despite known host
- **WHEN** a ref points at an allowlisted host but includes `?Expires=...&Signature=...`
- **THEN** the asset is baked, not kept external

### Requirement: Best-effort baking
A failed asset fetch (4xx/5xx, timeout, oversize, guard-blocked) MUST NOT fail the upload. The system SHALL keep the original absolute URL in the rewritten HTML and record the asset as `kept-external` in the manifest, together with the reason the fetch failed, so callers can surface why an asset is unresolved.

#### Scenario: Fetch failure tolerated
- **WHEN** an external image returns `403` during ingest
- **THEN** the upload still returns `201`, the HTML keeps the original URL, and the manifest records `kept-external` with reason `status 403`

### Requirement: Reference rewriting
The system SHALL rewrite every scanned, resolved reference to an origin-absolute path under `/a/{slug}/...` in both HTML and CSS before storage, so stored content contains no relative or external-origin dependency except deliberate `kept-cdn`/`kept-external` refs.

#### Scenario: Zip-relative ref rewritten
- **WHEN** entry HTML references `assets/hero.png` present in the zip
- **THEN** stored HTML references `/a/{slug}/assets/hero.png`

#### Scenario: Baked external ref rewritten
- **WHEN** an external image is successfully baked
- **THEN** stored HTML references `/a/{slug}/...` for it

### Requirement: Asset manifest
For every processed asset the system SHALL record: path, source URL, content type, byte size, and status (`local`, `baked`, `kept-cdn`, or `kept-external`), retrievable via `GET /api/pages/{slug}`.

#### Scenario: Manifest after mixed ingest
- **WHEN** an upload produces one zip-local asset, one baked asset, one kept-cdn, and one kept-external
- **THEN** the manifest lists all four with their correct statuses

### Requirement: Base-URL reference resolution
When processing a document with a base URL (URL imports), the system SHALL resolve relative references against the base — with a `<base href>` element overriding the base when present — and process the resolved absolute URLs through the existing classification (signed, keep-cdn allowlist, bake). Skip-class references (`data:`, fragments, `mailto:`, `javascript:`, …) remain skipped. Pack-local resolution applies only to uploaded packs and takes precedence there.

#### Scenario: Relative asset resolved and baked
- **WHEN** an imported page references `assets/hero.png` and the base is `https://example.com/post/`
- **THEN** the reference is fetched from `https://example.com/post/assets/hero.png`, stored, and rewritten to `/a/{slug}/external/example.com/post/assets/hero.png`

#### Scenario: Relative ref on allowlisted host kept external
- **WHEN** an imported page references `css/fonts.css` on a host on the keep-external allowlist
- **THEN** the resolved URL is kept as-is and recorded `kept-cdn`

#### Scenario: Base href overrides source URL
- **WHEN** an imported page declares `<base href="https://cdn.example.com/">` and references `img/logo.png`
- **THEN** the reference resolves against `https://cdn.example.com/img/logo.png`

#### Scenario: Skip-class refs untouched
- **WHEN** an imported page references `#anchor` or `data:image/png;base64,...`
- **THEN** the reference is left as-is and never fetched

### Requirement: Relative refs inside baked external stylesheets
References inside a baked external stylesheet SHALL be resolved against the stylesheet's own URL, fetched, baked, and rewritten, so a baked CSS file contains no dangling relative references.

#### Scenario: Font referenced relatively from baked CSS
- **WHEN** a baked stylesheet at `https://cdn.example.com/css/main.css` contains `@font-face { src: url(../fonts/a.woff2) }`
- **THEN** `https://cdn.example.com/fonts/a.woff2` is fetched and baked, and the stored CSS references `/a/{slug}/external/cdn.example.com/fonts/a.woff2`

#### Scenario: Unfetchable CSS ref recorded
- **WHEN** a relative reference inside a baked stylesheet fails to fetch
- **THEN** it is recorded `kept-external` with its resolved URL (and the strict gate applies on the import path)

