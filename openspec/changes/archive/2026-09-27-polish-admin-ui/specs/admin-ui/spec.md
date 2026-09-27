## ADDED Requirements

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
instead of always showing the input row. The row SHALL expand on click, and
automatically when the token is absent or a 401 is received. The token
remains stored in `localStorage` and a token entered after a 401 re-loads the
current view.

#### Scenario: token stored collapses the entry
- **WHEN** a token is present in `localStorage`
- **THEN** the header shows a state chip rather than the open input row

#### Scenario: 401 expands the entry with the error
- **WHEN** an API call returns 401
- **THEN** the token entry expands and displays the error message

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
