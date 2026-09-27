## ADDED Requirements

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
