## 1. Configuration

- [x] 1.1 `internal/config`: parse `AUTH_MODE` (default `token`) and implement the validation matrix — `token` requires `AUTH_TOKEN`; `none` rejects `AUTH_TOKEN`; `oidc` requires `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, `SESSION_SECRET` and tolerates optional `AUTH_TOKEN`; invalid mode names the valid values. Tests: table-driven cases per mode including serve-mode invariance and each failure naming its variable.

- [x] 1.2 `cmd/server` + `cmd/seed`: thread the mode and new vars through boot options; `seed` omits the `Authorization` header when `AUTH_TOKEN` is empty; log the `none`-mode unauthenticated-plane warning at boot. Acceptance: existing e2e suite stays green under the unchanged default; unit/boot test asserts the warning and the seed header behavior.

## 2. Auth core (`internal/auth`)

- [x] 2.1 Mode dispatcher + bearer check: `token` mode constant-time bearer compare (preserves D9 semantics), `none` allows all. Tests: table-driven accept/reject/`WWW-Authenticate` cases per mode.

- [x] 2.2 Session cookie mint/verify: base64 `{sub, email, exp}` payload, HMAC-SHA256 with `SESSION_SECRET`, `HttpOnly; Secure; SameSite=Lax; Path=/`, 12h lifetime. Tests: round-trip grants access; expired, tampered, and badly-signed cookies are unauthenticated.

- [x] 2.3 OIDC discovery: fetch `{issuer}/.well-known/openid-configuration`, enforce discovered-issuer == configured issuer, extract authorization/token/JWKS endpoints, fail fast on error. Tests: httptest fake IdP with fixture discovery documents; mismatch and unreachable-issuer errors.

- [x] 2.4 JWKS client + signature verification: RS256 and ES256 via stdlib, `kid`-keyed cache, refetch on unknown `kid`, 10-minute staleness bound, other algorithms rejected. Tests: in-test generated RSA/ECDSA keys served from an httptest JWKS endpoint; rotation and stale-cache cases.

- [x] 2.5 ID token claim validation: `iss`, `aud` = client ID, `exp`/`nbf` with ±60s skew, `nonce` match. Tests: signed tokens covering valid, expired, wrong audience, wrong issuer, HS256-rejected, wrong-nonce.

- [x] 2.6 Login flow handlers: `GET /login` (state+nonce in a 10-minute HttpOnly cookie, redirect to IdP), `GET /auth/callback` (state check, single-use state cookie, code exchange at the token endpoint, ID-token validation, session mint, redirect to `/`), `POST /logout` (clear session). Tests: full dance against the fake IdP; state mismatch, replayed state, and exchange-failure rejections.

## 3. Plane integration

- [x] 3.1 Replace the private bearer checks in `upload.Handler` and `lifecycle.API` with the shared `internal/auth` checker; add the `oidc`-mode CSRF rule (session-authenticated non-GET without `X-Requested-With: page-ui` → `403`; bearer-authenticated exempt). Tests: handler-level table tests per mode covering 401/403 paths and the machine-token path.

- [x] 3.2 `internal/serve` router: mount `/login`, `/auth/callback`, `/logout` in `oidc` mode only; gate the admin plane (`/`, `/api/*`) per mode; keep `/p/*`, `/a/*`, `/healthz` unauthenticated in every mode. Tests: route-matrix table tests across all three modes (admin vs serve instances).

- [x] 3.3 Admin UI: inject `data-auth-mode` into the shell HTML (server-side string replace); hide the token chip/row in `none`/`oidc` modes; navigate to `/login` on 401 in `oidc` mode; send `X-Requested-With: page-ui` on all API calls. Acceptance: served shell carries the attribute in each mode (handler test) and the UI behaviors are covered by the e2e journeys in 4.2.

## 4. End-to-end and docs

- [x] 4.1 Boot journeys in `internal/e2e`: `oidc` instance boots against an httptest IdP and fails fast on issuer mismatch/unreachable discovery; `none` instance boots without `AUTH_TOKEN`; `token` default unchanged. 

- [x] 4.2 E2E auth journeys: `token` mode (unchanged behavior), `none` mode (unauthenticated API works), `oidc` mode full flow (login → callback → session → list/upload with CSRF header → 403 without header → machine-token path → logout). Tests in `internal/e2e` with the fake IdP; no live network.

- [x] 4.3 Docs: README config table gains `AUTH_MODE` and the OIDC/`SESSION_SECRET` variables (with `openssl rand -hex 32` guidance) plus a deployment-shapes section (standalone / network-isolated / SSO); AGENTS.md design-invariant wording on auth placement updated to the shared checker. Acceptance: config table matches `internal/config` exactly.
