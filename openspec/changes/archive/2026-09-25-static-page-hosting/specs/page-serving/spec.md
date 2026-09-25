## ADDED Requirements

### Requirement: Page serving at /p/{slug}/
The system SHALL serve a page's entry HTML at `/p/{slug}/`, redirect `/p/{slug}` (no trailing slash) to `/p/{slug}/`, and return `404` for unknown slugs. No HTML processing may occur at serve time.

#### Scenario: Page retrieved
- **WHEN** `GET /p/landing-page-1/` is requested for an existing page
- **THEN** the response is `200` with the stored entry HTML

#### Scenario: Slashless URL redirected
- **WHEN** `GET /p/landing-page-1` is requested
- **THEN** the response redirects to `/p/landing-page-1/`

#### Scenario: Unknown slug
- **WHEN** `GET /p/does-not-exist-9/` is requested
- **THEN** the response is `404`

### Requirement: Database-free URL resolution
The serve path SHALL resolve request URLs to storage keys purely from the URL (`/p/{slug}/rest` → `{slug}/rest`, `/p/{slug}/` → `{slug}/index.html`, `/a/{slug}/rest` → `{slug}/rest`) and SHALL NOT query the database. Content types SHALL come from stored object metadata, not from database lookups or extension guessing at serve time.

#### Scenario: HTML request consults storage only
- **WHEN** `GET /p/landing-page-1/` is served
- **THEN** no database query occurs and the content type comes from the object's stored metadata

#### Scenario: Asset request consults storage only
- **WHEN** `GET /a/landing-page-1/assets/hero.png` is served
- **THEN** no database query occurs and the content type comes from the object's stored metadata

### Requirement: In-memory serving cache
The system SHALL cache served entry HTML in memory keyed by slug and SHALL serve repeat requests without a storage roundtrip. Cache entries MUST NOT expire or require invalidation (content is immutable); a byte-budget eviction MAY exist but correctness MUST NOT depend on it. Unknown slugs MUST NOT be negatively cached permanently — a page created after a miss MUST be served on its next request.

#### Scenario: Repeat request served from memory
- **WHEN** the same page URL is requested twice
- **THEN** the second response is served without a second storage `Get` (observable via a counting storage wrapper)

#### Scenario: Freshly uploaded page not blocked by stale misses
- **WHEN** a URL is requested before its page exists (404), then the page is uploaded
- **THEN** the next request for that URL returns `200` with the new page

### Requirement: Asset dual-mount
The system SHALL serve a page's assets under both `/a/{slug}/*` and `/p/{slug}/*`, mapping to the same storage prefix, so rewritten absolute refs and runtime-constructed relative refs both resolve.

#### Scenario: Asset via canonical prefix
- **WHEN** `GET /a/landing-page-1/assets/hero.png` is requested
- **THEN** the response is `200` with the stored bytes

#### Scenario: Asset via page prefix
- **WHEN** `GET /p/landing-page-1/assets/hero.png` is requested
- **THEN** the response is `200` with identical bytes

#### Scenario: Missing asset
- **WHEN** a stored page's HTML references an asset path that was never stored
- **THEN** the response is `404` and the page HTML itself still serves

### Requirement: Immutable caching headers
Page and asset responses SHALL carry long-lived immutable cache headers (`Cache-Control: public, max-age=31536000, immutable`) and an ETag, since page content never changes after upload.

#### Scenario: Cache headers present
- **WHEN** any `/p/{slug}/...` or `/a/{slug}/...` response is returned
- **THEN** it includes the immutable `Cache-Control` header and an `ETag`

#### Scenario: Conditional request
- **WHEN** a request repeats with `If-None-Match` matching the stored ETag
- **THEN** the response is `304` without a body

### Requirement: Correct content types
The system SHALL serve each object with the content type recorded at ingest (byte-sniffed at upload time) and `X-Content-Type-Options: nosniff` on every response; unknown types SHALL be served as `application/octet-stream`.

#### Scenario: CSS served correctly
- **WHEN** a stored stylesheet is requested
- **THEN** it is served as `text/css` with `nosniff`

#### Scenario: Unknown type not sniffed
- **WHEN** a stored file of unrecognized type is requested
- **THEN** it is served as `application/octet-stream` with `nosniff`
