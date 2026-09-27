## Context

Auth today is one static bearer token: `AUTH_TOKEN` required in admin/all modes, compared constant-time inside `upload.Handler.auth()` and `lifecycle.API.auth()` (static-page-hosting D9). The UI pastes the token into `localStorage` and sends it on every fetch; a 401 opens the token prompt. Production deployments front the admin plane with organizational SSO, which makes the token double friction for humans and undeliverable through proxies that rewrite `Authorization`.

Constraints from the repo: stdlib-first (no dependency without a direct consumer), no config knob without a scenario, code is flat and concrete, the serve path never touches the database, tests are table-driven with no live network in CI.

## Goals / Non-Goals

**Goals:**
- Support the three common deployment shapes as an OSS project: standalone (`token`), network-isolated behind a proxy (`none`), SSO via an OIDC IdP (`oidc`).
- Keep existing deployments byte-for-byte compatible (default `token`).
- Keep machines working: bearer token remains the machine path in `token` and `oidc` modes.
- Fail closed: every mode change is explicit config; no implicit open state.

**Non-Goals:** per-user authorization/roles, refresh tokens, proxy-assertion verification mode, basic auth, session revocation lists, SCIM, auth on the serve plane. See proposal.

## Decisions

**D1 — One `AUTH_MODE` switch, three values, default `token`.**
`token | none | oidc`. Validation: `token` requires `AUTH_TOKEN`; `none` forbids `AUTH_TOKEN` (strict — a mode that sometimes checks cannot be reasoned about) and logs a loud boot warning; `oidc` requires the OIDC vars + `SESSION_SECRET`, with `AUTH_TOKEN` optional (machine path).
*Alternatives considered:* silently making `AUTH_TOKEN` optional (the open state becomes implicit — today's required-token validation is the only thing catching "no token and no proxy"); defaulting to `none` (silently opens existing deployments).

**D2 — In-app OIDC authorization-code flow, not proxy-assertion verification.**
The app registers against any OIDC IdP (discovery, code flow, ID-token validation). Covers strictly more deployment shapes than verifying a fronting proxy's injected JWT (any IdP works, proxy optional; shops running oauth2-proxy have an IdP behind it anyway), and it is the shape OSS users expect (Gitea/Grafana/Harbor).
*Alternatives considered:* verify the proxy's JWT per request (IAP/CF Access/Pomerium-specific; requires a signing proxy; deferred as a future mode — the JWT-verification core built here is reusable); trust forwarded headers (spoofable whenever the app is directly reachable — the user's explicit dealbreaker); mTLS between proxy and app (TLS-architecture change, ops-heavy).

**D3 — Stateless HMAC-signed session cookie, not a session store.**
Cookie payload `{sub, email, exp}` base64-encoded, HMAC-SHA256 with `SESSION_SECRET`. `HttpOnly; SameSite=Lax; Path=/`, 12h fixed lifetime, and `Secure` whenever the request arrived over HTTPS (direct TLS or `X-Forwarded-Proto: https`) — behind a TLS-terminating proxy the app itself sees plain HTTP, and an unconditional Secure flag would break plain-HTTP dev deployments. No schema change, no sweep involvement, ~30 lines; revocation = secret rotation — acceptable for a single-admin-tenant tool. Expired/absent cookie → re-authenticate; no refresh machinery.
*Alternatives considered:* Postgres session table (revocable, but new schema, cleanup, and auth now depends on the DB); JWT access+refresh token pair (refresh is a non-goal; complexity without a scenario); unconditional `Secure` (breaks the legitimate plain-HTTP dev case for no gain — the flag is about browser transport, which the app can only observe per request).

**D4 — OIDC mechanics: discovery at boot, JWKS cached, RS256+ES256, strict claims.**
Boot fetches `{issuer}/.well-known/openid-configuration` and refuses to start if the discovered `issuer` ≠ configured issuer (mix-up defense) or discovery fails (fail fast, same as DB/storage boot failures). ID tokens validated: signature (RS256 and ES256 — both stdlib; HS256 rejected), `iss`, `aud` = client ID, `exp`/`nbf` with ±60s skew, `nonce` bound to the flow. JWKS fetched lazily on first verification, re-fetched on unknown `kid` (rotation), re-checked after 10 minutes. Fixed constants (12h session, 10m JWKS TTL, 60s skew) are deliberately not config knobs — no scenario needs them.
*Alternatives considered:* `golang-jwt`/`go-oidc` dependencies (a real consumer exists, but stdlib `crypto/rsa`/`crypto/ecdsa` + `encoding/json` covers it in ~150 lines and honors stdlib-first); static pubkey env var (Pomerium-style; breaks IdP key rotation for the major providers).

**D5 — Login dance: state cookie, single-use, fixed post-login redirect.**
`GET /login` generates `state` + `nonce`, stores them in a short-lived (10 min) `HttpOnly; SameSite=Lax` cookie, redirects to the IdP's auth endpoint (`response_type=code`, scope `openid email`). `GET /auth/callback` verifies state against the cookie, clears it (single use), exchanges the code at the token endpoint, validates the ID token, mints the session cookie, redirects to `/`. `POST /logout` clears the session cookie.
*Alternatives considered:* honoring the originally requested path post-login (nice, but adds state and an open-redirect surface; `/` is fine for a single-page admin UI); RP-initiated logout via the IdP's `end_session_endpoint` (extension point, not needed).

**D6 — CSRF: custom header on state-changing API calls in `oidc` mode.**
Cookie sessions introduce ambient credentials. `SameSite=Lax` already stops cross-site POSTs from carrying the cookie in modern browsers; as belt-and-suspenders, non-GET `/api/*` requests in `oidc` mode must carry `X-Requested-With: page-ui`. A custom header forces a CORS preflight, and the app sends no CORS headers, so cross-site JS is blocked. The UI always sends the header. `token` mode is unchanged — the bearer header itself is the CSRF shield.
*Alternatives considered:* per-request CSRF tokens (state, more machinery); `Origin` header checking (duplicates what SameSite + preflight already give).

**D7 — One shared checker at the admin-plane boundary; serve plane untouched.**
New `internal/auth` package owns mode dispatch: `none` → allow; `token` → bearer required (constant-time compare, D9 semantics preserved); `oidc` → valid session cookie OR valid bearer (machine path), plus the CSRF header rule on non-GET. `upload` and `lifecycle` handlers delegate to it instead of their private bearer checks. The router mounts `/login`, `/auth/callback`, `/logout` next to the admin plane; `/p/*`, `/a/*`, `/healthz` stay unauthenticated. Supersedes the per-handler placement of static-page-hosting D9; constant-time comparison is retained.
*Alternatives considered:* auth inside each handler as today (three mechanisms would need sharing anyway — the checker is the shared piece, not a new seam); middleware over the whole mux (would gate the serve plane — wrong).

**D8 — The UI learns its mode from the server, not by probing.**
The `ui` handler injects `data-auth-mode="<mode>"` into the embedded shell HTML (one string replace on our own static file). In `token` mode the UI is unchanged (chip, localStorage, 401 prompt). In `oidc` mode the chip is hidden and a 401 navigates to `/login`. In `none` mode the chip is hidden and no prompt can ever fire.
*Alternatives considered:* an extra mode-describing endpoint (a round trip to learn what the server already knew); a templating engine (overkill for one attribute).

**D9 — `cmd/seed` omits the Authorization header when `AUTH_TOKEN` is unset.**
Mechanical consequence of `none`/`oidc`-without-machine-path boots.

## Risks / Trade-offs

- [Session secret leak → full admin impersonation] → env-only storage, rotation documented in README, 12h expiry bounds the window.
- [IdP outage blocks admin boot (discovery at boot)] → fail fast matches existing boot behavior for DB/storage; the serve plane is unaffected; a machine-path `AUTH_TOKEN` keeps automation working once booted.
- [Clock skew vs the IdP] → ±60s tolerance on `exp`/`nbf`.
- [JWKS rotation race (request arrives signed by a new `kid`)] → refetch on unknown `kid`, 10m staleness bound.
- [Open redirect via login flow] → state cookie is single-use and HttpOnly; post-login redirect is the fixed path `/`.
- [XSS stealing the session] → cookie is HttpOnly; the UI has no third-party JS and escapes interpolation (`esc()`).
- [Stateless cookies cannot be revoked individually] → accepted for a single-admin tool; rotation revokes all sessions at once.

## Migration Plan

1. Ship with default `AUTH_MODE=token` — existing deployments change nothing.
2. Adopting SSO: register an OIDC client with redirect `{base}/auth/callback`, set the OIDC vars + `SESSION_SECRET` (`openssl rand -hex 32`), optionally keep `AUTH_TOKEN` for CI, flip `AUTH_MODE=oidc`.
3. Rollback is a config flip back to `token` (or unset). No data, schema, or storage migration involved.

## Open Questions

- Whether post-login should honor the originally requested path — deferred; fixed `/` until a scenario needs it.
