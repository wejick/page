## Verification: add-admin-management-ui (2026-09-26)

**Promise:** after this change, a user can see every published page in one
place and manage it — inspect status and asset manifest, take it down or
restore it, permanently remove it — from the web UI, without curl.
**Verdict:** delivered

Ran against the real system: dev Docker infra (MinIO + Postgres), the
all-mode server on :8080 (`make run`), plus serve (:8081) and admin (:8082)
instances for the plane contract. Evidence in
`/tmp/openspec-verify/add-admin-management-ui/` (transcripts + screenshots).

| Journey | Exercised intent | Verdict | Evidence |
|---|---|---|---|
| J1 publish → find | page-upload (API), admin-ui list/detail, page-management list | pass | j1-upload*.json, j1-render-evidence.txt |
| J2 token gate | admin-ui 401-prompt scenario | pass | j2-token-prompt.png |
| J3 takedown/restore | page-lifecycle via UI + bounded-staleness window | pass | j3-park-window.txt |
| J4 permanent removal | page-management delete + admin-ui confirm | pass | j4-delete.txt, j4-unknown-slug.png |
| J5 scale & conflict | pagination/status-filter + 409 + boot-sweep recovery | pass | j5-conflict.txt, j5-transient-ui.png |
| J6 plane contract | admin-ui serve-plane-404 scenario, deployment modes | pass | j6-planes.txt |

### Journey detail

- **J1**: uploaded `qa-mgmt-ui` packs via the documented API (201 + slug).
  UI list showed them newest-first with size/assets/status; detail showed the
  full manifest (`local`/`kept-cdn` per asset); the detail's **Open** link
  opened `/p/qa-mgmt-ui-2/`, which rendered with stylesheet applied
  (computed color proves `/a/` asset serving) and image decoded on the
  corrected pack (`qa-mgmt-ui-3`, `naturalWidth=1`).
- **J2**: with `localStorage` cleared, the UI showed "A valid API token is
  required." instead of an empty list (screenshot); typing the token loaded
  the list immediately, no reload needed.
- **J3**: parked `qa-mgmt-ui-3` from the list — badge flipped to `parked`,
  Open hidden, buttons became Unpark/Delete. Serving: assets `/a/…` 404'd
  immediately; entry `/p/…` stayed 200 inside the 60s revalidation window
  and 404'd after it (checked at +65s). Unpark from the UI restored 200 with
  rewritten refs, same URL.
- **J4**: detail-view Delete first **cancel** (confirm message captured,
  page untouched — 200), then **accept**: view returned to the list without
  the row; `/p/{slug}/`, `/a/{slug}/…` and `GET /api/pages/{slug}` all 404;
  re-DELETE and unknown-slug DELETE 404. A nonexistent slug's detail URL
  shows "Could not load …: 404 page not found" with a back link (screenshot).
- **J5**: created 55 pages via API → UI showed "Showing 1–50 of 64", Prev
  disabled; Next → "51–64", Next disabled, oldest page visible; Prev
  returned. Parking a page via API then selecting the `parked` filter
  showed exactly that page, total 1. Forcing a page into `parking`
  (simulated in-flight toggle): UI shows "parking…" badge with actions
  disabled and "A transition is in progress for this page." (screenshot);
  API `POST unpark` → 409 "opposite toggle in progress", `DELETE` → 409
  "lifecycle transition in progress". Restarting the all-mode server: boot
  sweep logged `resumed interrupted toggle slug=qa-mgmt-ui-2 status=parked`;
  API reports `parked`; page serves 404.
- **J6**: serve instance 404s `/`, `/api/pages`, `DELETE /api/pages/{slug}`
  while serving `/p/`, `/a/`, `/healthz`; admin instance serves `/`,
  401s `GET /api/pages`, park, and delete, 404s `/p/`, `/a/`; both healthy.

### Findings

None against the product. Two apparent smells were probed and resolved:

- Image not rendering on my first test page: the uploaded "PNG" was 8 magic
  bytes, not a decodable image; the asset itself served `200 image/png`
  byte-exact. Fixed the test data — image then decoded. (QA-data artifact.)
- 405/415 on perturbed uploads: initial probes used invalid input files
  (curl couldn't read a relative path; `x` isn't HTML so 415 fired before
  identifier validation). With a real HTML file, identifier
  `hello world/ünicode` → slug `hello-world-nicode-1` (serves 200) and
  `_parked` → `parked-1` (underscore stripped — cannot collide with the
  reserved prefix). Correct sanitization, not bugs.

### Exploratory notes

- Auth on the new endpoints: no/bad token → 401 on both list and delete
  (all-mode and admin-mode).
- Undocumented methods rejected: `PUT /api/pages/{slug}`,
  `DELETE /api/pages` (collection), `GET /api/pages/{slug}/park` → 405.
- Garbage list params degrade gracefully: `limit=abc&offset=-5` → 200 with
  defaults; `status=nonsense` → empty list, total 0 (unmatched filter —
  reasonable; spec doesn't demand 400).
- `limit=100000` clamps without error.
- Parked page assets (`/a/…`) 404 immediately; only entry HTML honors the
  revalidation window — matches the design's two-tier caching.
- Re-park is idempotent (200).
- Cleanup performed: 55 `qa-pager-*` pages and 2 identifier-test pages
  deleted via the API (all 404 afterwards, parked copies included).
- Harness limitations (not product issues): the embedded browser cannot
  drive the file chooser, so the upload form's picker happy-path was
  verified by form validation ("Choose a file first.") + the documented API
  instead; screenshots of popup tabs time out, so J1's rendered-page
  evidence is DOM/computed-style transcripts.

### Coverage

Delta scenarios not observable through the product surface (left to the
integration suite, all green): sweep-resumes-`deleting` (live run only
covered the park-resume path), delete-during-transition guard internals,
migration upgrade paths, counter untouched-by-delete, list clamp at the
500 cap with >500 rows.

### Environment left behind

- Stopped after the run: all-mode (:8080), serve (:8081), admin (:8082).
- Still running: dev Docker infra (MinIO :9000, Postgres :5432) — was
  running before verification.
- Leftover pages I created: `qa-mgmt-ui-2` (parked — demonstrates the
  parked state), `qa-mgmt-ui-3` (live); `sample-3…6` are from repeated
  `make seed` runs. Everything else deleted.
