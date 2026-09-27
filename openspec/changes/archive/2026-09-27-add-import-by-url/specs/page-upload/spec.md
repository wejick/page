## ADDED Requirements

### Requirement: Import by URL
The system SHALL accept a `url` form field on `POST /api/pages` as an alternative to the `file` field: exactly one of the two MUST be present. A URL import fetches the entry document server-side, then follows the same slug assignment, ingest, storage, and persistence path as a file upload, returning `201` with the same response shape. Requests with both fields MUST be rejected with `422`; requests with neither MUST be rejected with `400`. Authentication is unchanged (`401` without a valid token).

#### Scenario: Import succeeds
- **WHEN** a valid token holder POSTs `url=https://example.com/post` and the source serves an HTML page whose references all resolve
- **THEN** the API returns `201` with slug, page URL, and asset summary, and the page serves at `/p/{slug}/`

#### Scenario: Both sources given
- **WHEN** a request includes both a `file` and a `url` field
- **THEN** the API returns `422` and stores nothing

#### Scenario: No source given
- **WHEN** a request includes neither a `file` nor a `url` field
- **THEN** the API returns `400`

### Requirement: Entry-fetch failure semantics
A URL import whose entry fetch fails MUST be rejected before any storage occurs, with a status that distinguishes the cause: malformed URL or guard-blocked target → `422`; source unreachable, request timeout, or source `5xx` → `502`; response is not HTML → `415`; response exceeds the entry size cap → `413`.

#### Scenario: Source site is down
- **WHEN** the source URL cannot be reached or returns `5xx`
- **THEN** the API returns `502` and stores nothing

#### Scenario: Blocked target
- **WHEN** the source URL resolves to a private or link-local address
- **THEN** the API returns `422` and no outbound connection to the target is made

#### Scenario: Source is not HTML
- **WHEN** the source responds with a non-HTML document
- **THEN** the API returns `415` and stores nothing

#### Scenario: Entry over size cap
- **WHEN** the source response exceeds the entry size cap
- **THEN** the API returns `413` and stores nothing

### Requirement: Strict import completeness gate
For URL imports, the system SHALL reject the import with `422` when the ingest manifest contains any `kept-external` asset, returning the unresolved assets' source URLs with their failure reasons and advising the user to save the page in the browser and upload the file instead. Nothing is stored and no page is created. File uploads are unaffected and remain best-effort.

#### Scenario: All references resolve
- **WHEN** an import's manifest contains only `local`, `baked`, and `kept-cdn` assets
- **THEN** the API returns `201` and the page is created

#### Scenario: One asset fails to fetch
- **WHEN** an image on the source page returns `403` during import ingest
- **THEN** the API returns `422` with error `import_incomplete`, the image's URL and reason in `unresolved`, guidance to save-and-upload instead, and `GET /api/pages/{slug}` returns `404`

#### Scenario: Upload unaffected by the gate
- **WHEN** a file upload produces a `kept-external` asset
- **THEN** the API returns `201` as before (best-effort behavior unchanged)
