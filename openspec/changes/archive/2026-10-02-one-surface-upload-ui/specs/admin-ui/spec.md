## ADDED Requirements

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

## MODIFIED Requirements

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
