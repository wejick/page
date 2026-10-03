## 1. Asset foundation

- [x] 1.1 Vendor `@alpinejs/csp` (pinned 3.17.4) into `internal/serve/static/alpine.csp-3.17.4.min.js`; switch `serve.go` from `go:embed static/index.html` to `go:embed static` (directory). Acceptance: build green; embedded bytes verifiable in tests.
- [x] 1.2 Add a `/ui/{file}` handler serving the embedded assets: versioned Alpine file gets `Cache-Control: public, max-age=31536000, immutable`; `app.*` get `no-cache`; unknown files 404. Table-driven httptest covering content types, cache headers, and 404s. The old shell stays served at `/` unchanged.

## 2. Theme plumbing (server-side)

- [x] 2.1 `ui()` reads the `sp-theme` cookie (values `light`|`dark`|`system`, anything else treated as absent) and injects `data-theme` on the root element via the same string-replace mechanism as `data-auth-mode`; absent cookie injects nothing (system default). Table-driven httptest over cookie values → injected attribute. Acceptance: `GET /` with no cookie is byte-identical to today apart from the new attribute absence.

## 3. Shell rebuild on Alpine (no behavior drift)

- [x] 3.1 Shell skeleton: `index.html` becomes markup + Alpine directives (inline `<script>`/`<style>` removed); `app.js` registers the root `Alpine.data()` component — hash router, auth state (chip collapse/expand, 401 handling, oidc redirect, token in `localStorage`), `api()` fetch helper, flash; `app.css` carries the current light tokens. `x-cloak` until Alpine initializes. Acceptance: header chip, auth entry, and routing behave per the "Collapsible token entry" scenarios; all three views reachable.
- [x] 3.2 Port the list view: status filter, pagination window, badges (settled/transient), relative creation times with tooltips, park/unpark/delete with confirmation and 409 surfacing, empty state. Acceptance: "List view", "List pagination", "UI lifecycle and delete actions" scenarios pass in a driven browser.
- [x] 3.3 Port the detail view: metadata, manifest table, `kept-external` warning note, em-dash byte counts, actions per status. Acceptance: "Manifest presents ingest outcomes" scenarios pass.
- [x] 3.4 Port the upload view: one-surface source card (drop layer over whole card, URL footer as DOM sibling), last-action-wins, clear control, client-side extension check, identifier preview mirroring the slug sanitizer with reserved-set warning, publish/import flows with ingest summary and `import_incomplete` listing. Acceptance: "Upload accepts drag-and-drop", "Import by URL", "Mandatory identifier", "Upload result shows ingest summary" scenarios pass.

## 4. Dark mode and styling tidy

- [x] 4.1 Complete the semantic token set — add the missing members (danger button, drop highlight, input border, primary-button text, shadow/hover tints) and give every token light + dark values; dark palette also via `@media (prefers-color-scheme: dark)` (system) and `[data-theme="dark"]` (override); `color-scheme` matches. Acceptance: zero hardcoded colors outside the token block; both palettes AA-contrast.
- [x] 4.2 Theme toggle in the header cycling light → dark → system, writing the `sp-theme` cookie and updating `data-theme` live; hidden-state handling consistent with the auth chip. Acceptance: "Theme presentation" scenarios pass, including no flash of wrong theme on reload in all three states.
- [x] 4.3 Styling tidy: spacing/radii on a consistent 4px scale, unified `:focus-visible` rings (plus `:focus-within` on the drop layer), replace emoji icons with inline SVGs (optional, only if it stays small). Acceptance: visual pass shows no layout regressions at desktop and narrow widths ("Tables stay usable at narrow widths" scenario holds).

## 5. Security headers

- [x] 5.1 Admin-plane middleware setting `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'` and `X-Content-Type-Options: nosniff` on `GET /` and `/ui/*` responses (not on `/api/*`). httptest asserting the exact directives and the nosniff header. Sequencing note: lands only after group 3 — the policy bans the old shell's inline script/style.
- [x] 5.2 Confirm `app.js`/`app.css` contain nothing the policy blocks (no eval-dependent patterns, no string-form `:style` bindings — object syntax or `x-show` only). Acceptance: grep-level check + the 5.1 tests green with the real shell served.

## 6. Verification and docs

- [x] 6.1 Browser-driven verification pass: three views × two effective themes over a running instance (token and oidc modes for auth behavior), asserting the spec scenarios visible in the browser and **zero console errors / CSP violations**; repo gates green (`gofmt -l .` empty, `go vet ./...` and `go vet -tags=integration ./...` clean, `go test ./...` and `-tags=integration` green).
- [x] 6.2 Re-take README screenshots (list + upload views, light and dark); refresh the `admin-ui` spec Purpose line to match the new architecture when the change is archived (deltas don't carry Purpose edits).
