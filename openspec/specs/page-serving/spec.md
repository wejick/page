# page-serving Specification

## Purpose
DB-free byte serving on one origin: URL-to-key arithmetic, dual-mounted asset prefixes, split caching (immutable assets, revalidatable entry HTML), TTL-bounded in-memory HTML cache.

## Requirements

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
The system SHALL cache served entry HTML in memory keyed by slug and SHALL serve repeat requests from memory without a storage roundtrip until the entry is due for revalidation. Cache entries SHALL be revalidated against storage via `Stat` after a bounded TTL (`HTML_CACHE_TTL`, default 60 seconds, serving the page-lifecycle bounded-staleness requirement); a revalidation miss (page parked or removed) SHALL drop the entry and serve `404`. A byte-budget eviction MAY exist but correctness of serving MUST NOT depend on it. Unknown slugs MUST NOT be negatively cached permanently — a page created after a miss MUST be served on its next request.

#### Scenario: Repeat request served from memory
- **WHEN** the same page URL is requested twice within one revalidation TTL
- **THEN** the second response is served without a second storage roundtrip (observable via a counting storage wrapper)

#### Scenario: Parked page stops being served after revalidation
- **WHEN** a page's entry HTML is cached, the page is parked, and the revalidation TTL elapses
- **THEN** the next request for that URL returns `404` and the cache no longer holds the entry

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
Asset responses (`/a/{slug}/*` and asset paths under `/p/{slug}/*`) SHALL carry long-lived immutable cache headers (`Cache-Control: public, max-age=31536000, immutable`) and an ETag, since asset content never changes after upload. Entry HTML responses SHALL carry an ETag and a short-lived revalidatable policy (`Cache-Control: public, max-age=60, must-revalidate`) so lifecycle changes (parking) propagate through downstream caches within a bounded window; entry content itself still never changes while a page is live, so revalidation returns `304` unless the page was parked.

#### Scenario: Asset cache headers present
- **WHEN** an asset response is returned for `/a/{slug}/...` or an asset path under `/p/{slug}/...`
- **THEN** it includes the immutable `Cache-Control` header and an `ETag`

#### Scenario: Entry HTML cache header is revalidatable
- **WHEN** an entry HTML response is returned for `/p/{slug}/`
- **THEN** it includes an `ETag` and a `Cache-Control` with short `max-age` and `must-revalidate`, not `immutable`

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
