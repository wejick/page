## MODIFIED Requirements

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
