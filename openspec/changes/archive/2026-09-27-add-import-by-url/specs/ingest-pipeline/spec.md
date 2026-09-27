## ADDED Requirements

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

## MODIFIED Requirements

### Requirement: Best-effort baking
A failed asset fetch (4xx/5xx, timeout, oversize, guard-blocked) MUST NOT fail the upload. The system SHALL keep the original absolute URL in the rewritten HTML and record the asset as `kept-external` in the manifest, together with the reason the fetch failed, so callers can surface why an asset is unresolved.

#### Scenario: Fetch failure tolerated
- **WHEN** an external image returns `403` during ingest
- **THEN** the upload still returns `201`, the HTML keeps the original URL, and the manifest records `kept-external` with reason `status 403`
