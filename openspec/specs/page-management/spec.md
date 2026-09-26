# page-management Specification

## Purpose
List and hard-delete APIs over existing pages — the read-everything and
remove-everything surface of the admin plane. Listing is paginated and
status-filterable; deletion is a terminal lifecycle operation (intent
recorded in `pages.status`, objects removed idempotently from both the live
and `_parked/` prefixes, row last, crash-resumable by the boot sweep).
## Requirements
### Requirement: List pages via API
The system SHALL expose `GET /api/pages`, authenticated with the same bearer
token as the other admin APIs. It SHALL return a JSON object with `total`
(the number of pages matching the filter) and `pages`, an array of page
metadata objects (`slug`, `identifier`, `code`, `status`, `asset_count`,
`total_bytes`, `created_at`, `url`) ordered by `created_at` descending. It
SHALL accept `limit` (default 50, capped at 500) and `offset` query
parameters, and a `status` filter matching a lifecycle status value.

#### Scenario: List returns pages with total
- **WHEN** `GET /api/pages` is called with a valid token and three pages exist
- **THEN** the response is `200` with `"total": 3` and three page objects ordered newest first

#### Scenario: Pagination window
- **WHEN** `GET /api/pages?limit=2&offset=2` is called and five pages exist
- **THEN** the response contains the 3rd–4th newest pages and `"total": 5`

#### Scenario: Status filter
- **WHEN** `GET /api/pages?status=parked` is called with one parked and two live pages
- **THEN** only the parked page is returned, with `"total": 1`

#### Scenario: Limit above cap is clamped
- **WHEN** `GET /api/pages?limit=100000` is called
- **THEN** the response succeeds with at most 500 pages

#### Scenario: Unauthenticated list rejected
- **WHEN** `GET /api/pages` is called without a valid bearer token
- **THEN** the response is `401`

### Requirement: Delete a page via API
The system SHALL expose `DELETE /api/pages/{slug}`, authenticated with the
same bearer token. On success it SHALL remove the page's stored objects and
its database rows and respond `200`. Deletion SHALL record intent by
transitioning `pages.status` to `deleting` before removing any objects
(add-admin-management-ui D2), delete both `{slug}/` and `_parked/{slug}/`
prefixes idempotently, then delete the row (add-admin-management-ui D3).

#### Scenario: Delete a live page
- **WHEN** `DELETE /api/pages/{slug}` is called for a `live` page with a valid token
- **THEN** the response is `200`, no objects remain under `{slug}/`, the `pages` row and its assets are gone, and the slug counter state is untouched

#### Scenario: Delete a parked page
- **WHEN** `DELETE /api/pages/{slug}` is called for a `parked` page
- **THEN** the response is `200` and no objects remain under `_parked/{slug}/`

#### Scenario: Delete unknown slug
- **WHEN** `DELETE /api/pages/{slug}` is called for a slug that does not exist
- **THEN** the response is `404`

#### Scenario: Delete during a lifecycle transition conflicts
- **WHEN** `DELETE /api/pages/{slug}` is called while the page's status is `parking` or `unparking`
- **THEN** the response is `409`, nothing is deleted, and the page row remains

#### Scenario: Unauthenticated delete rejected
- **WHEN** `DELETE /api/pages/{slug}` is called without a valid bearer token
- **THEN** the response is `401` and nothing is deleted

### Requirement: Deleted pages stop serving
After a delete completes, requests to the deleted page's URLs SHALL return
`404` within the same revalidation window that bounds parked-page staleness;
no serve-plane changes SHALL be required (serving already resolves purely by
key existence).

#### Scenario: Deleted page 404s after revalidation
- **WHEN** a page's entry HTML was served and cached, the page is deleted, and the revalidation TTL elapses
- **THEN** `GET /p/{slug}/` returns `404`

### Requirement: Interrupted delete resumes
The admin-boot sweep SHALL resume a delete interrupted by a crash (status
`deleting`, objects or row possibly still present) to completion, re-running
the idempotent prefix deletions and removing the row.

#### Scenario: Sweep completes an interrupted delete
- **WHEN** the process crashed after `pages.status` became `deleting` but before the row was removed, and the admin process restarts
- **THEN** the sweep removes any remaining `{slug}/` and `_parked/{slug}/` objects, deletes the row, and the page no longer appears in `GET /api/pages`

