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
API calls SHALL use `fetch`, attaching the bearer token the user enters in
the UI (stored in `localStorage`, as the upload form does today) when the
auth mode is `token`, and relying on the session cookie plus the
`X-Requested-With` header required by the auth mode when it is `oidc`. The
UI shell SHALL learn its auth mode from a `data-auth-mode` attribute the
server injects into the page. The UI MUST NOT require any server-side
rendering or additional static assets beyond its single file.

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

#### Scenario: OIDC session works without any token entry
- **WHEN** the UI runs on an `oidc`-mode instance and the browser holds a valid session
- **THEN** all API calls succeed without any stored token and the list renders normally

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

### Requirement: Manifest presents ingest outcomes
The detail view's manifest SHALL present each asset's ingest status with
visual weight that matches its meaning: `kept-external` as a warning, the
other statuses as neutral. The manifest's status column SHALL be named
"Storage", distinct from the lifecycle Status shown in the list view.

#### Scenario: kept-external is a visible warning
- **WHEN** the manifest contains at least one asset with status `kept-external`
- **THEN** the UI styles that asset's status as a warning badge and renders a
  single explanatory note (assets still loading from their original site and
  may break) under the manifest table

#### Scenario: manifest without external assets has no warning
- **WHEN** the manifest contains no `kept-external` assets
- **THEN** no explanatory note is rendered

#### Scenario: unstored assets show no byte count
- **WHEN** an asset reports `0` bytes (kept on a CDN or external)
- **THEN** the UI shows an em dash instead of "0 B"

### Requirement: Upload result shows ingest summary
On a successful upload, the UI SHALL display the asset status counts returned
by the API alongside the new page's URL link, staying on the upload form.

#### Scenario: summary with counts
- **WHEN** an upload succeeds and the API reports asset counts (e.g. baked,
  local, kept external)
- **THEN** the success result shows those counts and links to the new page

#### Scenario: kept-external in the summary is a warning
- **WHEN** the reported counts include kept-external assets
- **THEN** the summary marks them with the same warning treatment as the
  manifest

### Requirement: Collapsible token entry
The UI SHALL collapse token entry into a compact chip that reflects state,
instead of always showing the input row — in `token` mode only. The row
SHALL expand on click, and automatically when the token is absent or a 401
is received. The token remains stored in `localStorage` and a token entered
after a 401 re-loads the current view. In `oidc` mode the chip and input row
SHALL be hidden and a 401 SHALL navigate the browser to `/login` to
re-authenticate; in `none` mode the chip and input row SHALL be hidden and
no prompt can appear.

#### Scenario: token stored collapses the entry
- **WHEN** a token is present in `localStorage` on a `token`-mode instance
- **THEN** the header shows a state chip rather than the open input row

#### Scenario: 401 expands the entry with the error
- **WHEN** an API call returns 401 on a `token`-mode instance
- **THEN** the token entry expands and displays the error message

#### Scenario: 401 in oidc mode navigates to login
- **WHEN** an API call returns 401 on an `oidc`-mode instance (e.g. an expired session)
- **THEN** the browser navigates to `/login` instead of showing token entry

#### Scenario: chip hidden without token auth
- **WHEN** the UI loads on a `none`- or `oidc`-mode instance
- **THEN** the token chip and input row are not rendered

### Requirement: Light theme presentation and interaction feedback
The UI SHALL render a pinned light color scheme with an explicit background,
styled title and links (no browser-default link colors), hover feedback on
interactive rows and buttons, visible keyboard focus, and muted text at WCAG
AA contrast on its background. Creation times SHALL render as relative
durations with the full locale datetime available as the tooltip. The list
SHALL hide a page's identifier when it is identical to the slug, and the
empty list state SHALL offer a link to the upload view.

#### Scenario: light scheme is pinned
- **WHEN** the page is rendered in a browser whose preferred scheme is dark
- **THEN** the UI still renders with its light palette and explicit
  background

#### Scenario: hover and focus feedback
- **WHEN** the user hovers a table row or button, or focuses an interactive
  element by keyboard
- **THEN** a visible state change is shown

#### Scenario: relative creation time
- **WHEN** the list or detail renders a creation time
- **THEN** it shows a relative duration (e.g. "1 day ago") whose tooltip
  holds the full locale datetime

#### Scenario: duplicate identifier hidden
- **WHEN** a page's identifier equals its slug
- **THEN** the list shows the slug without a repeated identifier line

#### Scenario: empty list offers upload
- **WHEN** the list contains no pages
- **THEN** the empty state includes a link that opens the upload view

### Requirement: Tables stay usable at narrow widths
List and manifest tables SHALL scroll horizontally within their container
rather than clipping the action column when the viewport is narrower than the
table's natural width.

#### Scenario: narrow viewport
- **WHEN** the list is viewed at a width too narrow for the full table
- **THEN** the table scrolls horizontally inside its container and the action
  buttons remain reachable

### Requirement: Upload accepts drag-and-drop
The upload view SHALL provide a styled drop zone that accepts a file by
drag-and-drop or click-to-browse, shows the chosen file's name and size, and
rejects file types outside `.html`, `.htm`, `.zip` client-side before any
request is sent. The submit path and server-side validation are unchanged.

#### Scenario: dropped file is selected
- **WHEN** the user drops an `.html` or `.zip` file onto the drop zone
- **THEN** the zone shows the file's name and size and the existing upload
  flow submits it

#### Scenario: unsupported file type rejected locally
- **WHEN** the user drops a file with an unsupported extension
- **THEN** the UI shows an error without sending a request

### Requirement: Import by URL in the upload view
The upload view SHALL offer a source-URL input as an alternative to file selection, submitting exactly one of the two to `POST /api/pages`. It SHALL surface entry-fetch failures with their cause (`422`/`415`/`413`/`502`) and, for `import_incomplete`, SHALL list the unresolved assets and display the save-and-upload guidance. The file-upload flow and its behavior are unchanged.

#### Scenario: Import via URL
- **WHEN** the user enters a URL, submits, and the API responds `201`
- **THEN** the view displays the new page's URL as a clickable link, as it does for uploads

#### Scenario: Incomplete import shows guidance
- **WHEN** the API responds `422` with `import_incomplete`
- **THEN** the view lists the unresolved assets and shows the guidance to save the page in the browser and upload the file instead, without losing the form state

#### Scenario: Entry-fetch error surfaces the cause
- **WHEN** the API responds `502` for an unreachable source
- **THEN** the view displays the API's error message indicating the source site could not be fetched

