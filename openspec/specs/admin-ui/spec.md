# admin-ui Specification

## Purpose
The management web UI served at `/` on the admin/all planes: a hand-written
HTML shell (no build step, no npm) rendered by the vendored Alpine.js CSP
build, with its scripts, styles, and the vendored framework embedded in the
binary and served same-origin from `/ui/*`, behind a strict
Content-Security-Policy. It gives list, detail, and upload views over the
page APIs, with bearer-token entry in the browser and a tri-state
light/dark/system theme. Covers what the user sees and does; the API
contracts themselves live in page-upload, page-management, and
page-lifecycle.
## Requirements
### Requirement: Management UI at the admin root
The system SHALL serve a management UI at `/` on the admin/all planes,
hand-written with no build step and no bundler, rendered through the
vendored Alpine.js CSP build. It SHALL provide three views: a page list, a
page detail, and the upload form. All API calls SHALL use `fetch`,
attaching the bearer token the user enters in the UI (stored in
`localStorage`) when the auth mode is `token`, and relying on the session
cookie plus the `X-Requested-With` header required by the auth mode when it
is `oidc`. The UI shell SHALL learn its auth mode from a `data-auth-mode`
attribute the server injects into the page. The shell's scripts and styles
SHALL be embedded in the binary and served same-origin from `/ui/*` — the
vendored Alpine file under a versioned file name with immutable caching,
the application assets with revalidation — and MUST NOT load anything from
a CDN or other external origin. No server-side rendering is introduced
beyond the existing attribute injection.

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

#### Scenario: Assets are embedded and same-origin only
- **WHEN** the UI shell loads in a browser
- **THEN** every script and stylesheet is served from the same origin under `/ui/*` with no request to any external origin, the versioned vendored file is served with immutable caching, and the application assets revalidate

#### Scenario: Untrusted values render escaped
- **WHEN** the list, detail, or upload view renders a page slug, identifier, asset path, or source URL
- **THEN** the value is rendered through the UI framework's escaping-by-default text binding rather than hand-escaped string concatenation into `innerHTML`

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

### Requirement: Tables stay usable at narrow widths
List and manifest tables SHALL scroll horizontally within their container
rather than clipping the action column when the viewport is narrower than the
table's natural width.

#### Scenario: narrow viewport
- **WHEN** the list is viewed at a width too narrow for the full table
- **THEN** the table scrolls horizontally inside its container and the action
  buttons remain reachable

### Requirement: Upload accepts drag-and-drop
The upload view SHALL present one source card whose top layer is a drop
zone accepting a file by drag-and-drop or click-to-browse across the whole
card, including over the URL input. A chosen file SHALL render as a summary
chip inside the drop layer showing the file's name and size, with a clear
control returning the card to its empty state. File types outside `.html`,
`.htm`, `.zip` SHALL be rejected client-side before any request is sent.
The submit path and server-side validation are unchanged.

#### Scenario: dropped file is selected
- **WHEN** the user drops an `.html` or `.zip` file onto the card (including
  onto the URL input area)
- **THEN** the drop layer shows the file's name and size as a summary chip
  and the existing upload flow submits it

#### Scenario: unsupported file type rejected locally
- **WHEN** the user drops a file with an unsupported extension
- **THEN** the UI shows an error without sending a request

#### Scenario: clear returns to empty state
- **WHEN** the user activates the summary chip's clear control
- **THEN** the card returns to its empty state and no source is submitted

### Requirement: Import by URL in the upload view
The upload view SHALL offer the source-URL input as the footer of the same
source card, submitting exactly one of URL or file to `POST /api/pages`.
The two inputs SHALL be exclusive by construction via last-action-wins:
typing a URL replaces an attached file, and attaching a file replaces a
typed URL, so the card always shows exactly the source that will be
published and the client never submits both. While a URL is set the drop
layer shows the URL as its summary chip; the input SHALL NOT be navigable
to a file-picker click by accident of nesting (the input is not a child of
the file label). The view SHALL surface entry-fetch failures with their
cause (`422`/`415`/`413`/`502`) and, for `import_incomplete`, SHALL list the
unresolved assets and display the save-and-upload guidance. The
file-upload flow and its behavior are unchanged.

#### Scenario: Import via URL
- **WHEN** the user enters a URL, submits, and the API responds `201`
- **THEN** the view displays the new page's URL as a clickable link, as it does for uploads

#### Scenario: Incomplete import shows guidance
- **WHEN** the API responds `422` with `import_incomplete`
- **THEN** the view lists the unresolved assets and shows the guidance to save the page in the browser and upload the file instead, without losing the form state

#### Scenario: Entry-fetch error surfaces the cause
- **WHEN** the API responds `502` for an unreachable source
- **THEN** the view displays the API's error message indicating the source site could not be fetched

#### Scenario: last action wins
- **WHEN** the user attaches a file and then types a URL (or enters a URL
  and then attaches a file)
- **THEN** the card shows only the most recent source, no "both sources"
  error is reachable, and Publish submits exactly that source

#### Scenario: clicking the URL input does not open the file picker
- **WHEN** the user clicks or focuses the URL input in the card footer
- **THEN** the file browser does not open and the input receives focus

### Requirement: Mandatory identifier with live URL preview
The upload view SHALL require an identifier before publishing: submitting
without one SHALL show an inline error and focus the field without sending
a request. As the identifier is typed, the view SHALL render a live preview
of the final URL shaped `/p/{sanitized}-{code}`, applying the same
normalization as the slug package (lowercase, `[a-z0-9-]`, collapsed and
trimmed dashes, 64-char cap). Reserved identifiers (the routing prefixes the
slug package reserves) SHALL render a warning instead of a preview and
block submission. The server-side API continues to accept an omitted
identifier; this requirement is a UI-only gate.

#### Scenario: preview mirrors the slug sanitizer
- **WHEN** the user types `My Cool Page!` into the identifier field
- **THEN** the preview shows `/p/my-cool-page-1`

#### Scenario: empty identifier blocks publish
- **WHEN** the user clicks Publish with an empty identifier
- **THEN** an inline error is shown, the field receives focus, and no
  request is sent

#### Scenario: reserved identifier warns and blocks
- **WHEN** the user types a reserved identifier (e.g. `api`)
- **THEN** the preview line shows a reserved-identifier warning and Publish
  does not send a request

### Requirement: Theme presentation and interaction feedback
The UI SHALL present a tri-state theme — light, dark, or system — with
system as the default. The user's choice SHALL persist across loads in a
`sp-theme` cookie, and the server SHALL inject the effective theme as a
`data-theme` attribute on the root element so the correct palette renders
before first paint (no flash of the wrong theme in any state). When no
choice is recorded, the dark palette SHALL apply via the browser's
`prefers-color-scheme`. The `color-scheme` property SHALL match the
effective theme so UA widgets (scrollbars, form controls, pickers) follow.
Both palettes SHALL meet WCAG AA contrast for text on their backgrounds and
SHALL come from one semantic token set with per-theme values. The UI SHALL
keep styled title and links (no browser-default link colors), hover
feedback on interactive rows and buttons, visible keyboard focus, and
unified focus rings. Creation times SHALL render as relative durations with
the full locale datetime available as the tooltip. The list SHALL hide a
page's identifier when it is identical to the slug, and the empty list
state SHALL offer a link to the upload view.

#### Scenario: system dark preference renders dark without a stored choice
- **WHEN** no `sp-theme` cookie is set and the browser's preferred scheme is dark
- **THEN** the UI renders the dark palette with matching `color-scheme` on first paint

#### Scenario: explicit choice persists and overrides the system
- **WHEN** the user picks a theme different from the system preference and reloads
- **THEN** the chosen palette renders on first paint, driven by the injected `data-theme` attribute, with no flash of the system palette

#### Scenario: toggle cycles the three states
- **WHEN** the user activates the theme toggle repeatedly
- **THEN** it cycles light → dark → system and the applied palette follows each state immediately

#### Scenario: hover and focus feedback
- **WHEN** the user hovers a table row or button, or focuses an interactive element by keyboard
- **THEN** a visible state change is shown in both themes

#### Scenario: relative creation time
- **WHEN** the list or detail renders a creation time
- **THEN** it shows a relative duration (e.g. "1 day ago") whose tooltip holds the full locale datetime

#### Scenario: duplicate identifier hidden
- **WHEN** a page's identifier equals its slug
- **THEN** the list shows the slug without a repeated identifier line

#### Scenario: empty list offers upload
- **WHEN** the list contains no pages
- **THEN** the empty state includes a link that opens the upload view

### Requirement: Strict security headers on the UI shell
Responses serving the UI shell and its assets (`GET /`, `GET /ui/*`) SHALL
carry a Content-Security-Policy whose default directive set is
`default-src 'none'`, permitting scripts and styles only from the same
origin, connections only to the same origin, and framing of the shell by no
one (`frame-ancestors 'none'`). The policy MUST NOT include
`unsafe-inline` or `unsafe-eval` for scripts or styles. The same responses
SHALL carry `X-Content-Type-Options: nosniff`. The policy is enforced (not
report-only) from deployment.

#### Scenario: shell response carries the policy
- **WHEN** `GET /` is served on an admin/all instance
- **THEN** the response includes `Content-Security-Policy` with `default-src 'none'`, `frame-ancestors 'none'`, and no `unsafe-inline`/`unsafe-eval` in `script-src` or `style-src`, and `X-Content-Type-Options: nosniff`

#### Scenario: shell renders fully under its own policy
- **WHEN** the UI loads in a browser with the policy enforced
- **THEN** all three views render with zero CSP violation reports — every script, style, and fetch the shell uses is covered by the policy

