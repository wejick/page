## Verification: polish-admin-ui (2026-09-27)

**Promise:** after this change, an operator can manage static pages through
the management UI while ingest outcomes are made legible — especially the
`kept-external` risk — the UI stays out of the way (token chip, light-theme
polish, relative timestamps), and tables stay usable at narrow widths.
**Verdict:** delivered

Environment: real stack — `make up` (MinIO + Postgres, already running),
`make run` (`mode=all`, `:8080`, token `devtoken`), real uploads through the
UI at `/#/upload`, real lifecycle actions, curl for HTTP-level evidence.
File-picker-driven selection is not drivable in the in-app browser, so file
selection was exercised through the drop zone with real fixture bytes
(legitimate: the drop handler is a specified surface); click-to-browse was
manually smoke-checked earlier against the mocked preview.

| Journey | Exercised intent | Verdict | Evidence |
|---|---|---|---|
| J1 first visit + token entry | 401 opens the token entry; real token loads the list and collapses it to a chip | pass | j1a-no-token-401.png, j1b-list-loaded.png |
| J2 upload golden path | drop zone accepts a real zip; success box shows ingest counts; following the URL renders the page with baked CSS and a decoding image | pass | j2-page-render.png, http-transcript.txt |
| J3 ingest risk legibility | real unfetchable asset → `kept-external`: amber summary line, amber badge, explanatory footnote, `—` instead of `0 B`, `Storage` column | pass | j3-risk-upload-summary.png, j3-risk-detail.png |
| J4 lifecycle round-trip | park from the list → `parked` UI state + 404 on page and assets; unpark → live + 200 | pass | j4-parked-list.png, http-transcript.txt |
| J5 delete | confirm dialog guards deletion; after accept the row disappears and URL/API 404 | pass | http-transcript.txt |
| J6 auth failure path | covered by J1a (real 401 → entry auto-opens focused with the message) | pass | j1a-no-token-401.png |
| J7 narrow viewport | 390px: table scrolls inside its container (596px content / 358px viewport) and actions are reachable after scroll | pass | j7-narrow-actions.png |

### Findings

None. No journey contradicted a delta scenario or the promise.

### Exploratory notes

- **Wrong extension (`.txt`) drop** → client-side rejection ("Unsupported file
  type"), nothing chosen, no request sent.
- **Empty HTML file** → server rejects with `415 Unsupported Media Type`; the
  message renders in the result box and the form state (identifier + chosen
  file) is preserved — spec scenario holds on a real rejection.
- **Unicode identifier** `qa polish ünicode` → slug `qa-polish-nicode-1`
  (non-ASCII dropped, spaces → dashes). Reasonable slugification; the result
  box shows the real slug so there is no surprise.
- **Double-submit** → native double-click on Upload issues exactly one POST
  (the disabled-button guard holds). A synthetic-event race (two Playwright
  clicks passing actionability inside the handler-start window) could fire
  two POSTs and create two pages; no real-browser path was identified — the
  handler sets `disabled` synchronously during the first click's dispatch.
  Automation-only artifact, noted not fixed.
- **Back navigation** after detail/success returns through hash history
  correctly; **repeat identifier** allocates the next slug
  (`qa-double-3`), counters move forward as designed.
- **Unknown slug detail** → "Could not load nope-nope: 404 page not found"
  with a back link.
- **API boundaries** → 401 (wrong/absent bearer), 405 (PUT), 404 (unknown
  slug, trailing-garbage path) — see http-transcript.txt.
- **Serving fidelity incident (fixture, not product):** the first J2 pack
  contained a hand-crafted, actually-invalid PNG; the browser showed a broken
  image while the server returned the bytes verbatim (`cmp` against the zip
  member: identical). Fixture defect; re-run with a valid PNG passed. This
  inadvertently confirmed ingest stores asset bytes untouched.
- **Parked assets** → direct `/a/{slug}/…` access returns 404 while parked.

### Coverage

Delta scenarios not observable through the product surface (left to
integration/e2e tests): transient status display (`parking`/`unparking`/
`deleting` badges — only exists after a crash before Sweep heals), the
serve-plane 404 contract of the UI (plane mounting is unchanged by this
change and covered by `internal/e2e/planes_test.go`), and the light-scheme
pinning on a dark-preference browser (verified earlier during apply QA by
rendering in the dark-canvas in-app browser before the explicit background
was added; the pinned `color-scheme: light` + explicit background makes it
static).

Leftover state created by this run (dev environment, harmless):
`sample-7` (seed), `qa-polish-ui-1` (contains the intentionally corrupt PNG),
`qa-polish-ui-2`, `qa-risk-1`, `qa-polish-nicode-1`, `qa-double-1..3`,
`qa-native-1`, `qa-native2-1`. The server started for this run was stopped;
Docker infra was already running and was left as found.
