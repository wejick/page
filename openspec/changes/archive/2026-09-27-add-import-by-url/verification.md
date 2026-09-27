## Verification: add-import-by-url (2026-09-27)

**Promise:** after this change, a user can paste a source URL into the upload
view (or POST it to the API); the server fetches the page, bakes its assets,
and publishes an immutable page — or publishes nothing and explains why,
advising save-and-upload.

**Verdict:** delivered

Real system: docker compose (MinIO + Postgres), server built from this
change's tree, `all` mode on :8080 with production config (`fetch.Standard`
guard). Journeys through the management UI (browser) and the documented API
(curl). One mid-run code fix is documented under Findings.

| Journey | Exercised intent | Verdict | Evidence |
|---|---|---|---|
| J1 golden import via UI | page-upload (Import by URL), admin-ui (URL input, published link) | pass | browser-evidence.txt (J1); `/p/qa-ui-1/` served rewritten example.com HTML (HTTP 200) |
| J2 source unreachable via UI | page-upload (Entry-fetch failure semantics → 502), admin-ui (error surfaces cause, form state kept) | pass | browser-evidence.txt (J2): "Error 502: could not fetch source url", inputs preserved |
| J3 guard blocks private source | web-fetch (Guarded outbound transport) | pass | j3-blocked.txt: self-import of `http://127.0.0.1:8080/...` → 422 "source url is not fetchable (blocked address)", nothing stored |
| J4 input contract (file XOR url) | page-upload (Import by URL: both → 422, neither → 400; scheme check) | pass | j4-both.txt (422), j4-neither.txt (400), j4-scheme.txt (422 for `file://`) |
| J5 strict completeness gate | page-upload (Strict import completeness gate), web-fetch (Shared guard covers asset baking), ingest-pipeline (kept-external reason) | pass | j5-gate2.json: public gist page with a loopback asset → 422 `import_incomplete`, unresolved lists "blocked address" + "status 404", `/p/qa-gate2-1/` → 404; UI renders the guidance + item list (browser-evidence.txt J5) |
| J6 regression smoke | page-upload (upload path unchanged) | pass | seeded page `/p/sample-8/` and `/p/sample-1/` serve; file-upload journeys covered by `TestUploadAndServeEndToEnd` (integration, green) |

### Findings

- [minor] blocked-asset reason in the 422 body carried the `url.Error`
  wrapper (`Get "http://…": fetch: blocked address: 127.0.0.1`) instead of a
  clean label — observed in the first gate run (j5-gate.json). Classification:
  code bug (D5 wants readable reasons). **Fixed during this run**
  (`fetchReason` maps the guard sentinel to "blocked address"), unit tests
  re-run green, and the gate journey was re-executed cleanly on the fixed
  binary — that re-run is the evidence above (j5-gate2.json).

### Exploratory notes

- Guard at the door: importing the app's own page (`http://127.0.0.1:8080/…`)
  is refused before any dial — the exfiltration-by-import path is closed
  (J3).
- Importing `https://github.com/` → 502: the source refused the entry fetch
  (bot detection). Real-world bot-blocked sites fail the entry fetch honestly
  rather than importing a challenge page in this case.
- Slug counters: seed landed on `sample-8` (codes 1–7 burned by prior dev
  runs) — counters-only-forward observable.
- Re-importing the same URL creates a new immutable page each time
  (`qa-import-1`, `qa-ui-1`); delete via the management API worked on both.
- Screenshot capture timed out twice (browser environment issue); DOM-state
  evidence was collected instead via `evaluate` and is quoted in
  browser-evidence.txt.

### Coverage

- Not browser-observable (left to integration tests, all green): redirect
  hops re-validated mid-flight (fetch package tests), charset normalization
  (fetch + e2e), oversize/timeout boundaries (upload integration tests),
  `<base href>` precedence and external-CSS relative-ref baking (pipeline
  unit tests + golden regression), park/unpark interplay (existing e2e).
- The gate journey needed a real source page with an unfetchable asset; a
  public gist referencing a loopback asset provided it deterministically with
  the production guard.
