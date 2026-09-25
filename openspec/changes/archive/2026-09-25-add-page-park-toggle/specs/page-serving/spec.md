# page-serving Delta

## MODIFIED Requirements

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
