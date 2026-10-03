## Why

The admin UI is the only browser surface in the product and it holds the API
bearer token in `localStorage`, yet it ships with no security headers and
renders untrusted strings (slugs, identifiers, manifest paths) through
hand-escaped `innerHTML` templates — one missed `esc()` call is XSS with the
token as the prize. The same file pins a light-only palette
(polish-admin-ui D1), so dark-OS users get a white page with UA widgets to
match, and the accumulated string-template boilerplate makes safe rendering
a discipline rather than a default.

## What Changes

- Rebuild the UI shell on Alpine.js — vendored CSP build (`@alpinejs/csp`,
  no CDN): declarative bindings replace `innerHTML` string templates;
  `x-text` escapes by default.
- Split the single file into four embedded assets (`index.html`, `app.css`,
  `app.js`, `alpine.csp-<version>.min.js`) served same-origin from a new
  `/ui/` route; the `data-auth-mode` server-injection hook is preserved.
- Add a strict Content-Security-Policy (`default-src 'none'`, no
  `unsafe-inline`/`unsafe-eval`) plus `X-Content-Type-Options: nosniff` on
  UI shell responses, enforced immediately.
- Replace the pinned light theme with a tri-state light/dark/system toggle:
  an `sp-theme` cookie drives server-side `data-theme` injection (no flash
  of wrong theme, no inline script), with `prefers-color-scheme` as the
  system default.
- Tidy the styling: complete the semantic token set (the hardcoded
  stragglers — danger red, drop-highlight blue, input borders, rgba tints —
  become tokens), consistent spacing/radii, unified focus rings.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `admin-ui`: the single-file/no-framework/no-additional-assets requirement
  is replaced (Alpine + embedded external assets served at `/ui/`); the
  pinned-light-theme requirement is replaced by a tri-state theme
  requirement; new requirement for strict security headers on the UI shell.

## Impact

`internal/serve/static/*` (restructure + vendored Alpine — the first
frontend dependency, justified against the dependency rule in design.md);
`internal/serve/serve.go` (`ui()`: cookie-driven theme injection, `/ui/`
asset route, header middleware; `go:embed static` directory). No API
contract, storage, database, or auth changes. README screenshots need
re-taking. Verification is browser-driven: three views × two themes with
console-error checks.

## Non-goals

- CSP on user pages (`/p/*`, `/a/*`): uploaded HTML may legitimately inline
  scripts; policing that belongs to the edge, not the admin plane.
- Origin separation between admin UI and user pages (same-origin
  session-cookie exposure in oidc mode) — an infrastructure change.
- CSP violation-reporting endpoints; report-only rollout.
- Reskin or redesign: interactions, information architecture, and wire
  behavior are unchanged; every existing admin-ui scenario keeps passing.
