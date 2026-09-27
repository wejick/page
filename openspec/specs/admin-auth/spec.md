# admin-auth Specification

## Purpose
TBD - created by archiving change auth-modes. Update Purpose after archive.
## Requirements
### Requirement: Auth mode selects the admin auth mechanism
The system SHALL support an `AUTH_MODE` environment variable with values `token`, `none`, or `oidc` (default `token`) that selects how the admin plane (`/`, `/api/*`) authenticates requests. An invalid or unknown mode SHALL fail startup with a descriptive error. The default `token` mode SHALL preserve the existing single-token behavior exactly.

#### Scenario: Default mode preserves token behavior
- **WHEN** an admin/all instance starts without `AUTH_MODE` set
- **THEN** it behaves as `token`: every admin-plane request requires the static bearer token

#### Scenario: Invalid mode fails fast
- **WHEN** an instance starts with `AUTH_MODE=kerberos`
- **THEN** startup fails with an error naming the valid modes, before opening any network listener

### Requirement: Token mode accept rule
In `token` mode every admin-plane API request SHALL be authenticated by the static bearer token, compared constant-time against `AUTH_TOKEN`. Requests without a matching token SHALL receive `401` with a `WWW-Authenticate: Bearer` header. The UI shell at `/` stays public, exactly as before: a browser navigation cannot carry the header, and the API-driven 401 prompt is the auth UX.

#### Scenario: Valid bearer is accepted
- **WHEN** a request carries `Authorization: Bearer <AUTH_TOKEN>`
- **THEN** the admin-plane handler proceeds normally

#### Scenario: Missing or wrong bearer is rejected
- **WHEN** a request carries no Authorization header or a non-matching token
- **THEN** the response is `401` with a `WWW-Authenticate: Bearer` header and no handler logic runs

### Requirement: None mode accept rule
In `none` mode the admin plane SHALL accept every request without credentials, and the instance SHALL log a warning at boot stating that the admin plane is unauthenticated. `none` mode SHALL NOT accept `AUTH_TOKEN` (startup fails if it is set).

#### Scenario: Unauthenticated request succeeds
- **WHEN** a request with no credentials reaches the admin plane of a `none`-mode instance
- **THEN** the admin-plane handler proceeds normally

#### Scenario: Boot warns about the open plane
- **WHEN** a `none`-mode instance boots
- **THEN** a warning stating the admin plane is unauthenticated appears in the logs

### Requirement: OIDC login flow
In `oidc` mode the system SHALL act as an OIDC relying party using authorization-code flow. At boot it SHALL fetch `{OIDC_ISSUER}/.well-known/openid-configuration`, fail startup if discovery fails or the discovered `issuer` differs from the configured `OIDC_ISSUER`, and use the discovered authorization, token, and JWKS endpoints. `GET /login` SHALL redirect to the IdP's authorization endpoint with `state` and `nonce` bound to a short-lived HttpOnly cookie. `GET /auth/callback` SHALL verify the `state` against that cookie, clear it (single use), exchange the code at the token endpoint, validate the ID token, mint the session cookie, and redirect to `/`. `POST /logout` SHALL clear the session cookie. These routes SHALL exist only in `oidc` mode.

#### Scenario: Unauthenticated browser is redirected to login
- **WHEN** a request without a session reaches `GET /` on an `oidc`-mode instance
- **THEN** the response is a redirect to `/login`, and `/login` redirects to the IdP's authorization endpoint

#### Scenario: Successful callback establishes a session
- **WHEN** the IdP redirects to `/auth/callback` with a code and the state cookie matches
- **THEN** the app exchanges the code, sets the session cookie, clears the state cookie, and redirects to `/`

#### Scenario: State mismatch is rejected without a session
- **WHEN** `/auth/callback` is called with a `state` that does not match the state cookie, or without the state cookie
- **THEN** the response is `401`, no session cookie is set, and the state cookie is cleared

#### Scenario: Logout clears the session
- **WHEN** an authenticated user sends `POST /logout`
- **THEN** the session cookie is cleared and subsequent admin-plane requests are unauthenticated

### Requirement: ID token validation
The system SHALL validate the ID token's signature against the IdP's JWKS (RS256 and ES256; any other algorithm rejected), that `iss` matches the configured issuer, `aud` matches `OIDC_CLIENT_ID`, `exp`/`nbf` within ±60 seconds of clock skew, and that `nonce` matches the one bound at `/login`. JWKS keys SHALL be cached and re-fetched when an unknown `kid` is encountered.

#### Scenario: Valid ID token establishes the session
- **WHEN** the callback exchanges a code for an ID token satisfying all claim checks
- **THEN** the session cookie is minted carrying the token's subject and email

#### Scenario: Expired or wrongly-audience ID token is rejected
- **WHEN** the ID token has an expired `exp`, a foreign `aud`, or an unexpected `iss`
- **THEN** the login fails with an error and no session cookie is set

#### Scenario: Key rotation does not break login
- **WHEN** the ID token is signed with a key whose `kid` is not in the cached JWKS
- **THEN** the JWKS is re-fetched and, if the key is found there, login succeeds

### Requirement: Session cookie
In `oidc` mode the session SHALL be a stateless cookie whose payload (`sub`, `email`, `exp`) is base64-encoded and HMAC-SHA256-signed with `SESSION_SECRET`. The cookie SHALL be `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` whenever the request arrived over HTTPS (direct TLS or `X-Forwarded-Proto: https`), with a 12-hour lifetime; the server SHALL NOT store session state. A request whose cookie is absent, expired, tampered, or badly signed SHALL be treated as unauthenticated.

#### Scenario: Valid cookie grants access
- **WHEN** a request carries a well-signed, unexpired session cookie
- **THEN** the admin-plane handler proceeds normally

#### Scenario: Expired cookie is unauthenticated
- **WHEN** a request carries a well-signed session cookie whose `exp` has passed
- **THEN** the request is treated as unauthenticated (redirect for browser routes, `401` for API routes)

#### Scenario: Tampered cookie is rejected
- **WHEN** a request carries a cookie whose payload does not match its HMAC
- **THEN** the request is treated as unauthenticated

#### Scenario: Cookie security attributes track the transport
- **WHEN** the login flow sets cookies over HTTPS (direct TLS or `X-Forwarded-Proto: https`)
- **THEN** the cookies carry the `Secure` attribute; over plain HTTP they do not, so plain-HTTP dev deployments still work

### Requirement: Machine token path in oidc mode
In `oidc` mode, when `AUTH_TOKEN` is configured, a request presenting the matching bearer token SHALL be accepted without a session cookie. When `AUTH_TOKEN` is unset, only session-authenticated requests are accepted.

#### Scenario: Bearer authenticates without a session
- **WHEN** a request carries `Authorization: Bearer <AUTH_TOKEN>` and no session cookie on an `oidc`-mode instance configured with `AUTH_TOKEN`
- **THEN** the admin-plane handler proceeds normally

#### Scenario: Neither proof is rejected
- **WHEN** a request to `/api/*` carries neither a valid session cookie nor the bearer token
- **THEN** the response is `401`

### Requirement: CSRF rule for session-authenticated state changes
In `oidc` mode, a state-changing (`non-GET`) request authenticated by the session cookie SHALL be rejected with `403` unless it carries the custom header `X-Requested-With: page-ui`. Bearer-authenticated requests SHALL be exempt. The UI SHALL send the header on all its API calls.

#### Scenario: Cookie-authenticated POST without the header is rejected
- **WHEN** a cross-site request submits `POST /api/pages` riding a valid session cookie and no `X-Requested-With` header
- **THEN** the response is `403` and no upload occurs

#### Scenario: UI calls carry the header
- **WHEN** the management UI issues a non-GET API call in `oidc` mode
- **THEN** the request includes `X-Requested-With: page-ui` and is accepted

### Requirement: Unauthenticated surface is unchanged
In every auth mode, `/healthz` and the serving plane (`/p/*`, `/a/*`) SHALL remain accessible without credentials.

#### Scenario: Health and pages stay public
- **WHEN** unauthenticated requests hit `/healthz` and `/p/{slug}/` on an `oidc`-mode instance
- **THEN** both respond normally without any session or token

