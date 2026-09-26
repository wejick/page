# admin-ui Specification

## Purpose
The management web UI served at `/` on the admin/all planes: a single
hand-written HTML file (no build step, no framework) giving list, detail,
and upload views over the page APIs, with bearer-token entry in the
browser. Covers what the user sees and does; the API contracts themselves
live in page-upload, page-management, and page-lifecycle.
## Requirements
### Requirement: Management UI at the admin root
The system SHALL serve a management UI at `/` on the admin/all planes as a
single hand-written HTML file with no build step and no framework. It SHALL
provide three views: a page list, a page detail, and the upload form. All
API calls SHALL use `fetch` with the bearer token the user enters in the UI
(stored in `localStorage`, as the upload form does today). The UI MUST NOT
require any server-side rendering or additional static assets beyond its
single file.

#### Scenario: List view shows all pages
- **WHEN** the user opens `/` with a valid token entered
- **THEN** the list view renders every page's slug, status, size, asset count, and creation time, newest first, from `GET /api/pages`

#### Scenario: List pagination
- **WHEN** the list response reports more pages than one window holds
- **THEN** the UI offers prev/next controls that fetch the adjacent window via `limit`/`offset`

#### Scenario: Detail view shows metadata and manifest
- **WHEN** the user opens a page's detail view
- **THEN** the UI shows the page's metadata and its full manifest from `GET /api/pages/{slug}`, including each asset's path and status

#### Scenario: Upload view preserves form behavior
- **WHEN** the user submits the upload form with a valid pack
- **THEN** the UI displays the new page's URL as a clickable link, and on rejection displays the API's error message without losing the form state

#### Scenario: Missing or bad token surfaces the error
- **WHEN** the token is absent or rejected (401)
- **THEN** the UI prompts for the token instead of rendering an empty list or a silent failure

### Requirement: UI lifecycle and delete actions
The list and detail views SHALL offer park/unpark for `live`/`parked` pages
and delete for any settled page, calling the corresponding admin APIs and
refreshing the view from the API response afterwards. Delete MUST require an
explicit confirmation before the request is sent. The UI SHALL reflect
transient statuses (`parking`, `unparking`, `deleting`) as visible states
with their actions disabled.

#### Scenario: Park from the UI
- **WHEN** the user clicks park on a live page and the API responds `200`
- **THEN** the view refreshes and the page shows the `parked` status

#### Scenario: Delete requires confirmation
- **WHEN** the user clicks delete
- **THEN** the UI asks for confirmation first and only sends `DELETE /api/pages/{slug}` after the user confirms; on success the page disappears from the list

#### Scenario: Busy page is not actionable
- **WHEN** a page's status is `parking` or `unparking` (e.g. a `409` from a racing toggle)
- **THEN** the UI shows the transient state and surfaces the conflict message instead of leaving the button silently dead

### Requirement: UI stays on the admin plane
The management UI and its API calls SHALL follow the existing plane
mounting: served at `/` on admin/all, absent (404) on the serve plane, with
all `/api/*` calls likewise unreachable from the public tier.

#### Scenario: Serve plane does not expose the UI
- **WHEN** `GET /` is requested on a serve-mode instance
- **THEN** the response is `404` and no management surface is reachable

