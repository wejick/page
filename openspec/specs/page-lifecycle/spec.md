# page-lifecycle Specification

## Purpose
Park/unpark lifecycle toggle for pages: move a page's objects to a reserved prefix so it stops serving, restore it byte-identically at the same URL, with bounded cache staleness, a durable status state machine, and no slug counter side effects.

## Requirements

### Requirement: Park a page via API
The system SHALL expose `POST /api/pages/{slug}/park`, authenticated by the same bearer token as other admin API routes. Parking SHALL move the page's objects from the `{slug}/` prefix to the reserved `_parked/{slug}/` prefix, after which the page's entry HTML and assets SHALL NOT be served (`404` on `/p/{slug}/…` and `/a/{slug}…`). Requests without a valid token MUST be rejected with `401`; unknown slugs MUST be rejected with `404`; parking an already-parked page SHALL be idempotent (`200`).

#### Scenario: Page parked and no longer served
- **WHEN** a valid token holder parks an existing page, and the park request returns
- **THEN** the response is `200` with status `parked`, and `GET /p/{slug}/` and `GET /a/{slug}/index.html` return `404`

#### Scenario: Unauthenticated park
- **WHEN** `POST /api/pages/{slug}/park` is called without a valid bearer token
- **THEN** the API returns `401` and no objects move

#### Scenario: Unknown slug
- **WHEN** a valid token holder parks a slug that does not exist
- **THEN** the API returns `404`

### Requirement: Unpark restores the page at the same URL
The system SHALL expose `POST /api/pages/{slug}/unpark` (same auth rules) that moves objects back to `{slug}/`, restoring the page at its original slug with byte-identical content, content types, and a **stable ETag** equal to the pre-park ETag. Unparking a live page SHALL be idempotent (`200` with status `live`).

#### Scenario: Park and unpark roundtrip
- **WHEN** a page is parked and then unparked, and `GET /p/{slug}/` is requested with the pre-park `If-None-Match` ETag
- **THEN** the page serves `200` with byte-identical content, and the ETag matches the pre-park ETag (a conditional request with it returns `304` before re-upload semantics differ)

#### Scenario: Idempotent unpark
- **WHEN** a live page is unparked
- **THEN** the API returns `200` with status `live` and objects are unchanged

### Requirement: Bounded staleness for parked pages
Once a park request has completed, a page that was cached (in the in-memory entry cache or downstream) MUST stop being served within the entry-revalidation window (default 60 seconds): subsequent requests SHALL return `404`, not cached bytes. Correctness of the parked state MUST NOT depend on caches beyond this window.

#### Scenario: Cached entry HTML stops serving after revalidation
- **WHEN** a page's entry HTML was served and cached, the page is parked, and the revalidation TTL elapses
- **THEN** the next `GET /p/{slug}/` returns `404`, not the cached bytes

### Requirement: Durable toggle state machine
The system SHALL record lifecycle status in `pages.status` (`live`, `parking`, `parked`, `unparking`) before mutating any objects, finalize it after the move completes, and serialize concurrent toggles via guarded status transitions. Statuses left in `parking`/`unparking` by a crash SHALL be resumed to completion by re-invoking the toggle or by a sweep at admin boot; resume SHALL be idempotent (copies and deletes are idempotent, door ordering preserves correctness).

#### Scenario: Interrupted park resumes
- **WHEN** a park is interrupted after the status is recorded (objects split between prefixes) and the toggle is re-invoked or the admin process restarts
- **THEN** the move completes to `parked` (or `live`), no objects are lost, and the door state (entry HTML presence) is never contradictory with the recorded transition direction

### Requirement: Lifecycle status is visible via the manifest API
`GET /api/pages/{slug}` SHALL include the page's lifecycle `status` field alongside the existing manifest data.

#### Scenario: Manifest reports parked
- **WHEN** `GET /api/pages/{slug}` is requested for a parked page
- **THEN** the response is `200` with the manifest and `"status": "parked"`

### Requirement: Parking is slug-neutral
Park and unpark SHALL NOT allocate, consume, or rewind slug codes: `counters` and the `(identifier, code)` uniqueness are untouched by toggling, and uploads with the same identifier while a page is parked continue from the next code.

#### Scenario: Upload while another page of the identifier is parked
- **WHEN** `landing-page-1` is parked and a new page is uploaded with identifier `landing-page`
- **THEN** the new page receives slug `landing-page-2` and serves normally
