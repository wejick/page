## ADDED Requirements

### Requirement: Upload via ajax API
The system SHALL expose `POST /api/pages` accepting multipart form data with a file (single `.html` or `.zip`) and an optional identifier, authenticated by a bearer token, returning `201` with the assigned slug, page URL, and asset summary. Requests without a valid token MUST be rejected with `401`.

#### Scenario: Single HTML upload
- **WHEN** a valid token holder POSTs a single `.html` file with identifier `landing-page`
- **THEN** the API returns `201` with slug `landing-page-1` and the page URL

#### Scenario: Zip pack upload
- **WHEN** a valid token holder POSTs a `.zip` containing `index.html` plus assets
- **THEN** the API returns `201` with a slug and all contained assets stored

#### Scenario: Unauthenticated upload
- **WHEN** `POST /api/pages` is called without a valid bearer token
- **THEN** the API returns `401` and no page is created

#### Scenario: Identifier omitted
- **WHEN** a valid upload omits the identifier
- **THEN** the system assigns a default identifier and still returns `201` with a valid slug

### Requirement: Upload validation and limits
The system SHALL enforce upload caps (max raw size, max decompressed size, max file count) and zip-safety checks, rejecting violations with a descriptive `4xx` before any storage occurs.

#### Scenario: Oversize upload
- **WHEN** an upload exceeds the configured size cap
- **THEN** the API returns `413` and stores nothing

#### Scenario: Zip path traversal
- **WHEN** a zip contains an entry named with `..` or an absolute path
- **THEN** the API returns `422` and stores nothing

#### Scenario: Nested zip
- **WHEN** a zip contains another zip entry
- **THEN** the API returns `422` and stores nothing

### Requirement: Minimal upload UI
The system SHALL serve a single HTML page at `/` that uploads via `fetch` with `FormData`, and displays the resulting page URL on success or the error message on failure. The UI MUST NOT require any build step or framework.

#### Scenario: Successful upload shows URL
- **WHEN** a user drops a valid pack onto the upload page and submits
- **THEN** the page displays the new page's URL as a clickable link

#### Scenario: Failed upload shows error
- **WHEN** an upload is rejected (e.g. bad token, oversize)
- **THEN** the page displays the API's error message without losing the form state
