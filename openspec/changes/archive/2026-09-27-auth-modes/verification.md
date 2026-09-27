## Verification: auth-modes (2026-09-27)

**Promise:** after this change, a deployer can match admin-plane auth to
their deployment — `token` (static bearer, unchanged default), `none`
(network/SSO proxy is the boundary), or `oidc` (browser SSO against an OIDC
IdP, bearer kept as the machine path) — while pages (`/p/*`, `/a/*`) and
`/healthz` stay public in every mode.

**Verdict:** delivered

Live run: `make up` stack (real MinIO + Postgres, already running), four
instances of the real `cmd/server` binary — token `:8080`, oidc `:8081`,
none `:8082`, serve `:8083` — plus a standalone OIDC IdP on `:7777`
(discovery, sign-in page, token endpoint with client-credential check,
JWKS). Browser journeys driven through the in-app browser; API journeys via
curl transcripts. Teardown: all five processes stopped; compose stack left
as found. Leftover slugs: `qa-oidc-machine-1` (parked), `sample-9` (seed).

| Journey | Exercised intent | Verdict | Evidence |
|---|---|---|---|
| J1 token-mode UI: list renders with stored token; clearing it → 401 expands the token entry with "A valid API token is required."; entering the token re-loads the view (23 rows, chip flips to "Token set") | admin-ui (modified), admin-auth token mode | pass | `j1a-token-list.png`, `j1b-401-prompt.png` |
| J2 browser SSO: `GET /` unauthenticated → 302 `/login` → IdP sign-in page (full OIDC contract in the query: client_id, nonce, redirect_uri, scope, state) → click "Sign in" → callback → landing on the UI; `data-auth-mode="oidc"`, token chip hidden, list renders via session cookie | admin-auth OIDC login flow, admin-ui (modified) | pass | `j2-sso-landing.png` |
| J2b CSRF: session-cookie `POST …/park` without `X-Requested-With` → 403; with the header (what the UI sends) → 200 `{"status":"parked"}` | admin-auth CSRF rule | pass | `j2-retry.txt` |
| J2c logout: `POST /logout` → 200, session cleared, next API call 401 | admin-auth logout | pass | `j2-retry.txt` |
| J6 machine path: bearer-only upload (`ci-token`, no session) → 201 with ingest summary | admin-auth machine token path | pass | `j6-upload.json` |
| J3 none mode: UI renders (`data-auth-mode="none"`, chip hidden) and API answers 200 with zero credentials; unauthenticated upload 201 | admin-auth none mode, deployment-modes | pass | `j3-none-landing.png` |
| J4 plane/mode matrix: serve instance 404s `/` and `/api/*` but serves `/p/sample-9/`; every instance answers `/healthz`; oidc instance gates `/`→302 and `/api/*`→401 while `/p/*` stays public | deployment-modes, admin-auth unauthenticated surface | pass | `j4j5-curl.txt` |
| J5 token-mode API gate: no token → 401 + `WWW-Authenticate: Bearer`; wrong token → 401; correct → 200; `/login` unmounted (404) | admin-auth token accept rule | pass | `j4j5-curl.txt` |

### Findings

None. No `fail`, no `degraded`, no intent gap.

### Exploratory notes

- Parked page hidden on the public plane of the oidc instance
  (`/p/qa-oidc-machine-1/` → 404 unauthenticated) — auth does not interfere
  with the takedown contract.
- Forged session cookie (`page_session=ZXZpbA.forged-mac`) → API 401,
  browser 302 `/login`.
- `/auth/callback` without the state cookie → 401; with
  `?error=access_denied` → 401; state replay after the cookie is cleared
  (single use) covered by unit tests.
- Undocumented methods: `PUT /api/pages`, `DELETE /login`, `GET /logout` →
  405 (router method-scoping intact under the new routes).
- 405-vs-403 seen once during the run was a QA script error (GET to a
  POST-only route without `-X POST`), not product behavior; corrected and
  re-run clean.
- Boot-time behavior (discovery fail-fast on unreachable/mismatched issuer,
  none-mode boot warning, seed without token) exercised at the subprocess
  level in `internal/e2e` (`TestOIDCBootJourney`, `TestNoneModeBootWarns`,
  `TestSeedWithoutToken`) and not repeated in the browser run.

### Coverage

Delta scenarios not observable through the product surface (left to the
unit/integration suites, all green):

- ID-token claim validation matrix (expired/wrong aud/iss/nonce, HS256
  rejection, ES256, JWKS rotation refetch) — `internal/auth` unit tests.
- Session cookie attribute details (Secure only over HTTPS, HMAC tamper
  matrix, wrong-secret rejection, 12h expiry) — `internal/auth` unit tests.
- Config validation matrix (AUTH_MODE × required/forbidden variables,
  serve-mode invariance) — `internal/config` unit tests.
- Concurrent lifecycle/state internals — unchanged by this change;
  pre-existing integration coverage.
