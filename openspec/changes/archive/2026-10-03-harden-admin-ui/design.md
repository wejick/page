## Context

The admin UI (`internal/serve/static/index.html`) is a single 28 KB
`go:embed`'d file: ~500 lines of vanilla JS that renders three hash-routed
views (list, detail, upload) by building HTML in JS template strings,
hand-escaping every interpolated value (`esc()`), and re-wiring event
handlers after each `innerHTML` swap. It is the only browser surface in the
product (`/login` in oidc mode is a pure 302) and the only place the API
bearer token is exposed to the browser (`localStorage`). It ships no
security headers, and its light-only palette is a deliberate prior decision
(polish-admin-ui D1) made when colors were hardcoded — the same change's D2
has since tokenized most of the palette.

Constraints that shape this design: the admin plane runs on an internal
network/VPN (deployment-modes), so nothing may load from a CDN; the repo
rule admits a dependency only with a direct consumer; there are no
browser-level tests today, so behavior scenarios in `admin-ui/spec.md` are
the regression contract; `serve.go` injects the auth mode by string-replacing
`data-auth-mode="token"`.

## Goals / Non-Goals

**Goals:**

- Make safe rendering the default, not a discipline: untrusted values reach
  the DOM through escaping-by-default bindings.
- Put a strict, enforced Content-Security-Policy on the UI shell.
- Dark mode with a light/dark/system toggle and no flash of wrong theme.
- Tidy styling: complete semantic tokens, consistent spacing/radii, unified
  focus rings.
- Zero behavior drift: every existing admin-ui scenario keeps passing; no
  API, storage, database, or auth changes.

**Non-Goals:**

- CSP on user pages (`/p/*`, `/a/*`) — uploaded HTML may inline scripts;
  that is an edge concern.
- Origin separation of admin UI from user pages (same-origin session-cookie
  exposure in oidc mode) — infrastructure change, noted as adjacent risk.
- CSP violation reporting, report-only rollout.
- Redesign: information architecture, interactions, and wire behavior
  unchanged.

## Decisions

**D1 — Alpine.js, CSP build, vendored into the binary.** Rebuild the shell
on `@alpinejs/csp` (pinned version, file name carries it:
`alpine.csp-3.17.4.min.js`), fetched same-origin, all component state
registered via `Alpine.data()` in `app.js`. Serves the safe-rendering
requirement: `x-text` escapes by default, deleting the `esc()` hazard
class. Alternatives: stay vanilla (rejected — the standing XSS-ergonomics
cost grows with every view; this is the direct consumer the dependency rule
demands); standard Alpine build (rejected — expression evaluation via
`new Function()` would break under the CSP this change adds); CDN load
(rejected — internal/VPN deployments must not phone out; also a supply-chain
trust transfer).

**D2 — Four embedded assets, `/ui/` route.** `static/` becomes `index.html`
(markup + Alpine directives only), `app.css`, `app.js`, and the vendored
Alpine file; `go:embed static` replaces the single-file embed; `serve.go`
grows a `/ui/{file}` handler. Cache: versioned Alpine gets
`max-age=31536000, immutable`; `app.*` get `no-cache` (tiny files, admin
plane, no invalidation bugs). The `data-auth-mode` injection is preserved —
`app.js` reads it from the DOM. Alternatives: keep one file with CSP
hashes/nonce for inline script+style (rejected — every edit needs hash
recomputation or per-request nonce machinery; silent breakage mode);
keep one file without CSP (rejected — CSP is a goal).

**D3 — Strict CSP, enforced immediately.**
`default-src 'none'; script-src 'self'; style-src 'self'; connect-src
'self'; img-src 'self' data:; base-uri 'none'; form-action 'none';
frame-ancestors 'none'` on UI shell responses, plus
`X-Content-Type-Options: nosniff`. Consequences adopted: no inline script
or style anywhere; `@click` attributes are fine (listeners attach via
`addEventListener`, CSP build evaluates without eval); `x-show`/object-form
`:style` only (string `:style` goes through `setAttribute` and would be
blocked by `style-src`). Alternatives: `Content-Security-Policy-Report-Only`
first (rejected — needs a reporting endpoint for marginal value on a
single-binary admin tool; browser verification covers enforcement);
allow `unsafe-eval` (rejected — defeats the point).

**D4 — Tri-state theme via `sp-theme` cookie, injected server-side.**
Toggle cycles light → dark → system; the choice persists in an
`sp-theme` cookie read by `ui()`, which injects `data-theme` into the HTML
alongside `data-auth-mode` (same string-replace mechanism). CSS has the
three standard cases: light default, `@media (prefers-color-scheme: dark)`
when no attribute (system), `[data-theme="dark"]` override. `color-scheme`
is set to match so UA widgets (scrollbars, `<select>` popups) follow. No
flash of wrong theme in any state, and no inline `<head>` script (which D3
bans). Alternatives: pre-paint inline script + localStorage (rejected —
banned by our own policy; hash-recoupling on every edit); system-only with
no toggle (rejected — mirrors the D1 complaint it is meant to fix, in
reverse); `light-dark()` CSS function (rejected — redundant once a manual
toggle requires the attribute mechanism anyway).

**D5 — Semantic tokens only; complete the set; dark palettes are values.**
The existing semantic layer (`--bg`, `--text`, `--warn-bg`, …) gains the
missing members (danger button, drop-highlight, input border, primary-on
text, shadow/hover tints) and each token gets a light and a dark value; the
dark palette is ~a dozen value overrides, not per-component work — this is
what shrinks polish-admin-ui D1's "doubles the visual test surface" to a
bounded cost, so D1 is superseded. Spacing/radii consolidate on a 4px-based
scale; focus rings unify on `:focus-visible` (plus `:focus-within` for the
drop layer). Alternatives: two-tier primitive+semantic vocabulary
(rejected — design-system grammar this file's size doesn't need);
keep hardcoded stragglers (rejected — they are exactly what breaks in dark).

**D6 — Verification is browser-driven.** No browser tests exist; a rewrite
this size rewrites every view. Verification: scripted browser pass over the
three views × two effective themes, asserting visible behavior from the
spec scenarios and zero console errors under CSP; unit/`httptest` coverage
for the new server surface (`/ui/` route, headers, cookie injection). README
screenshots re-taken. Alternatives: manual click-through only (rejected —
CSP breakage is exactly the class of thing console errors reveal);
headless E2E suite as permanent infrastructure (rejected — beyond the
smallest thing; not requested).

## Risks / Trade-offs

- [Alpine does nothing until its script runs; broken templates render
  blank] → `x-cloak` on the shell until Alpine initializes; browser
  verification checks every view renders.
- [CSP build restricts expressions to registered `Alpine.data()` scope] →
  accepted; it forces the cleaner all-logic-in-`app.js` structure anyway.
- [Vendored file drifts from upstream / quietly ages] → version is in the
  filename and tasks pin the exact release; upgrading is a deliberate file
  swap.
- [Dark palette doubles the visual surface] → bounded by D5 to token
  values; verification exercises both themes.
- [Strict CSP blocks something discovered late] → the policy is
  enumerate-allow, so a gap is a visible console error with a named
  directive; fixes are one-line allows, not redesigns.
- [Token in `localStorage` remains readable by same-origin user pages in
  single-instance mode] → pre-existing exposure, unchanged by this design;
  real fix is edge origin separation (non-goal).

## Migration Plan

Same binary, no configuration change, no data migration. One deploy: the
new shell replaces the old at `/`, assets arrive with the same embed.
Rollback is reverting the deploy — the old file is intact in the prior
binary and no persistent state (cookie is optional and default-safe).
After deploy: re-take README screenshots.
