## MODIFIED Requirements

### Requirement: Management UI at the admin root
The system SHALL serve a management UI at `/` on the admin/all planes,
hand-written with no build step and no bundler, rendered through the
vendored Alpine.js CSP build. It SHALL provide three views: a page list, a
page detail, and the upload form. All API calls SHALL use `fetch`,
attaching the bearer token the user enters in the UI (stored in
`localStorage`) when the auth mode is `token`, and relying on the session
cookie plus the `X-Requested-With` header required by the auth mode when it
is `oidc`. The UI shell SHALL learn its auth mode from a `data-auth-mode`
attribute the server injects into the page. The shell's scripts and styles
SHALL be embedded in the binary and served same-origin from `/ui/*` — the
vendored Alpine file under a versioned file name with immutable caching,
the application assets with revalidation — and MUST NOT load anything from
a CDN or other external origin. No server-side rendering is introduced
beyond the existing attribute injection.

#### Scenario: List view shows all pages
- **WHEN** the user opens `/` with a valid token entered
- **THEN** the list view renders every page's slug, status, size, asset count, and creation time, newest first, from `GET /api/pages`

#### Scenario: List pagination
- **WHEN** the list response reports more pages than one window holds
- **THEN** the UI offers prev/next controls that fetch the adjacent window via `limit`/`offset`

#### Scenario: Detail view shows metadata and manifest
- **WHEN** the user opens a page's detail view
- **THEN** the UI shows the page's metadata and its full manifest from `GET /api/pages/{slug}`, including each asset's path and status

#### Scenario: Upload view preserves form behavior
- **WHEN** the user submits the upload form with a valid pack
- **THEN** the UI displays the new page's URL as a clickable link, and on rejection displays the API's error message without losing the form state

#### Scenario: Missing or bad token surfaces the error
- **WHEN** the token is absent or rejected (401)
- **THEN** the UI prompts for the token instead of rendering an empty list or a silent failure

#### Scenario: OIDC session works without any token entry
- **WHEN** the UI runs on an `oidc`-mode instance and the browser holds a valid session
- **THEN** all API calls succeed without any stored token and the list renders normally

#### Scenario: Assets are embedded and same-origin only
- **WHEN** the UI shell loads in a browser
- **THEN** every script and stylesheet is served from the same origin under `/ui/*` with no request to any external origin, the versioned vendored file is served with immutable caching, and the application assets revalidate

#### Scenario: Untrusted values render escaped
- **WHEN** the list, detail, or upload view renders a page slug, identifier, asset path, or source URL
- **THEN** the value is rendered through the UI framework's escaping-by-default text binding rather than hand-escaped string concatenation into `innerHTML`

## REMOVED Requirements

### Requirement: Light theme presentation and interaction feedback
**Reason**: The pinned light scheme (polish-admin-ui D1) is superseded by a
tri-state theme with a dark palette; the requirement is replaced wholesale
by "Theme presentation and interaction feedback", which retains its
interaction-feedback and list-presentation scenarios unchanged.
**Migration**: The new requirement carries every scenario this one had,
except "light scheme is pinned", which is intentionally reversed.

## ADDED Requirements

### Requirement: Theme presentation and interaction feedback
The UI SHALL present a tri-state theme — light, dark, or system — with
system as the default. The user's choice SHALL persist across loads in a
`sp-theme` cookie, and the server SHALL inject the effective theme as a
`data-theme` attribute on the root element so the correct palette renders
before first paint (no flash of the wrong theme in any state). When no
choice is recorded, the dark palette SHALL apply via the browser's
`prefers-color-scheme`. The `color-scheme` property SHALL match the
effective theme so UA widgets (scrollbars, form controls, pickers) follow.
Both palettes SHALL meet WCAG AA contrast for text on their backgrounds and
SHALL come from one semantic token set with per-theme values. The UI SHALL
keep styled title and links (no browser-default link colors), hover
feedback on interactive rows and buttons, visible keyboard focus, and
unified focus rings. Creation times SHALL render as relative durations with
the full locale datetime available as the tooltip. The list SHALL hide a
page's identifier when it is identical to the slug, and the empty list
state SHALL offer a link to the upload view.

#### Scenario: system dark preference renders dark without a stored choice
- **WHEN** no `sp-theme` cookie is set and the browser's preferred scheme is dark
- **THEN** the UI renders the dark palette with matching `color-scheme` on first paint

#### Scenario: explicit choice persists and overrides the system
- **WHEN** the user picks a theme different from the system preference and reloads
- **THEN** the chosen palette renders on first paint, driven by the injected `data-theme` attribute, with no flash of the system palette

#### Scenario: toggle cycles the three states
- **WHEN** the user activates the theme toggle repeatedly
- **THEN** it cycles light → dark → system and the applied palette follows each state immediately

#### Scenario: hover and focus feedback
- **WHEN** the user hovers a table row or button, or focuses an interactive element by keyboard
- **THEN** a visible state change is shown in both themes

#### Scenario: relative creation time
- **WHEN** the list or detail renders a creation time
- **THEN** it shows a relative duration (e.g. "1 day ago") whose tooltip holds the full locale datetime

#### Scenario: duplicate identifier hidden
- **WHEN** a page's identifier equals its slug
- **THEN** the list shows the slug without a repeated identifier line

#### Scenario: empty list offers upload
- **WHEN** the list contains no pages
- **THEN** the empty state includes a link that opens the upload view

### Requirement: Strict security headers on the UI shell
Responses serving the UI shell and its assets (`GET /`, `GET /ui/*`) SHALL
carry a Content-Security-Policy whose default directive set is
`default-src 'none'`, permitting scripts and styles only from the same
origin, connections only to the same origin, and framing of the shell by no
one (`frame-ancestors 'none'`). The policy MUST NOT include
`unsafe-inline` or `unsafe-eval` for scripts or styles. The same responses
SHALL carry `X-Content-Type-Options: nosniff`. The policy is enforced (not
report-only) from deployment.

#### Scenario: shell response carries the policy
- **WHEN** `GET /` is served on an admin/all instance
- **THEN** the response includes `Content-Security-Policy` with `default-src 'none'`, `frame-ancestors 'none'`, and no `unsafe-inline`/`unsafe-eval` in `script-src` or `style-src`, and `X-Content-Type-Options: nosniff`

#### Scenario: shell renders fully under its own policy
- **WHEN** the UI loads in a browser with the policy enforced
- **THEN** all three views render with zero CSP violation reports — every script, style, and fetch the shell uses is covered by the policy
