## ADDED Requirements

### Requirement: Guarded outbound transport
The system SHALL perform all server-side web fetches (entry imports and asset baking) through a guarded transport that: allows only `http` and `https` schemes; resolves the target host and rejects loopback, RFC1918 private, link-local (including `169.254.169.254`), unique-local, CGNAT, unspecified (`0.0.0.0`), and multicast addresses; re-validates every redirect hop against the same rules; and dials the validated IP address for the request. A fetch to a blocked target MUST fail with a typed error and MUST NOT open a connection.

#### Scenario: Public fetch allowed
- **WHEN** a fetch targets a public `https://` URL
- **THEN** the request proceeds and the response body is returned

#### Scenario: Private address blocked
- **WHEN** a fetch targets a host that resolves to a loopback, RFC1918, or link-local address (including `169.254.169.254`)
- **THEN** the fetch fails with a blocked-target error and no page or asset is produced from it

#### Scenario: Redirect into private space blocked
- **WHEN** a fetch to a public URL redirects to a host that resolves to a private address
- **THEN** the redirect hop is refused and the fetch fails with a blocked-target error

#### Scenario: Non-http scheme rejected
- **WHEN** a fetch is requested with a scheme other than `http` or `https` (e.g. `file://`)
- **THEN** the fetch fails validation before any dial

### Requirement: Bounded entry fetch
The entry fetcher SHALL fetch a single document with a per-request timeout and a hard size cap on the response body, returning the fetched bytes and the final URL after redirects. Oversize responses and timeouts MUST surface as typed errors, not partial results.

#### Scenario: Oversize response rejected
- **WHEN** the source responds with more bytes than the entry size cap
- **THEN** the fetch fails with an oversize error and the buffered body is discarded

#### Scenario: Final URL reflects redirects
- **WHEN** the source URL redirects (e.g. `/post` → `/post/`)
- **THEN** the fetch result carries the final URL as the page's resolution base

### Requirement: Entry HTML validation
The entry fetcher SHALL reject a response that is not an HTML document, determined by content sniffing consistent with the upload path's HTML detection.

#### Scenario: Non-HTML response rejected
- **WHEN** the source responds with a JSON or PDF body
- **THEN** the fetch fails with a not-HTML error

### Requirement: Charset normalization
The entry fetcher SHALL normalize the fetched document to UTF-8 based on the response's declared charset, so downstream parsing operates on UTF-8 regardless of the source's encoding.

#### Scenario: Non-UTF-8 source normalized
- **WHEN** the source declares `charset=iso-8859-1` and serves encoded text
- **THEN** the fetched bytes are UTF-8 and non-ASCII characters render correctly in the imported page

### Requirement: Shared guard covers asset baking
The asset-baking fetcher SHALL use the same guarded transport, so an asset reference that fails the guard is treated like any other failed fetch.

#### Scenario: Uploaded HTML referencing an internal URL
- **WHEN** an uploaded page references `http://10.0.0.5/secret.png` and ingest attempts to bake it
- **THEN** the fetch fails the guard, the asset is recorded `kept-external`, and no object is stored from the internal target
