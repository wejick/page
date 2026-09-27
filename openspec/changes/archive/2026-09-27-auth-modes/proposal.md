## Why

The only auth today is one static `AUTH_TOKEN` bearer, required on every admin/all boot. Production deployments front the admin plane with organizational SSO, which makes the static token double friction for humans (SSO login, then paste a token) and undeliverable through proxies that rewrite `Authorization`. As an open-source project the deployment shapes vary: standalone (token), network-isolated behind a reverse proxy (no in-app auth), and SSO via an OIDC provider. Auth must support all three instead of assuming the first.

## What Changes

- New `AUTH_MODE` configuration selecting the admin-plane auth mechanism:
  - `token` (default, unchanged behavior): static bearer required per request; `AUTH_TOKEN` required.
  - `none`: no in-app auth; boot logs a loud warning; deployer owns protection via network/SSO proxy. Strict: `AUTH_TOKEN` must be unset.
  - `oidc`: the app is an OIDC relying party — discovery, authorization-code flow, ID-token validation against the IdP's JWKS — and mints a stateless HMAC-signed session cookie. Unauthenticated browser traffic to `/` redirects to `/login`; `/api/*` returns 401. The bearer token remains accepted as the machine path when `AUTH_TOKEN` is set.
- New browser routes on the admin plane: `/login`, `/auth/callback`, `/logout`.
- CSRF defense in `oidc` mode (cookie sessions introduce ambient credentials): state-changing API calls must carry a custom header the UI already sends.
- The admin UI becomes mode-aware: token chip/401 prompt only in `token` mode; in `oidc` mode a 401 navigates to `/login`.
- Serve plane untouched: `/p/*` and `/a/*` stay public; `healthz` stays unauthenticated.

## Capabilities

### New Capabilities
- `admin-auth`: the auth mode switch and per-request accept rules — mode validation, bearer check, OIDC flow, session cookie, machine token path, CSRF rule.

### Modified Capabilities
- `deployment-modes`: config requirements become conditional on `AUTH_MODE` (which vars are required/forbidden per mode, boot warning in `none`).
- `admin-ui`: token entry and the 401 prompt become conditional on auth mode; `oidc` mode redirects to `/login`.

## Impact

- `internal/config`: new vars (`AUTH_MODE`, `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, `SESSION_SECRET`), validation matrix.
- New `internal/auth` package: mode dispatch, bearer check (constant-time, preserves D9 for `token` mode), OIDC flow, cookie minting/verification, JWKS caching. Stdlib only (`crypto/rsa`, `crypto/hmac`, `net/http`) — no new dependency.
- `internal/upload`, `internal/lifecycle`: handlers delegate auth to the shared checker instead of private bearer checks.
- `internal/serve`: mounts the browser auth routes; UI shell advertises the mode.
- `cmd/seed`: omits empty Authorization header.
- No database schema change (sessions are stateless).

## Non-goals

- No per-user authorization, roles, or multi-tenancy — any authenticated identity is a full admin.
- No refresh-token machinery; an expired session re-authenticates via the IdP.
- No proxy-assertion verification mode (verifying a fronting proxy's injected JWT) — a possible future addition; the JWT-verification core built here is reusable for it.
- No basic auth, no session revocation list (revocation = `SESSION_SECRET` rotation), no SCIM/provisioning.
- No auth on the serve plane.
