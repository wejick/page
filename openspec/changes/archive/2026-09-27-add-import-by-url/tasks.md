# Tasks — add-import-by-url

Testing note: unit tests exercise HTTP sources with `httptest` on loopback,
which the SSRF guard blocks by design — fetcher tests use the injectable
permissive guard (import-by-url D3). No live network anywhere.

## 1. `internal/fetch` — guarded transport and entry fetcher

- [x] 1.1 Guarded transport (D3): scheme allowlist (http/https), host resolution with private-range rejection (loopback, RFC1918, link-local incl. `169.254.169.254`, ULA, CGNAT, `0.0.0.0`, multicast), redirect policy re-validating every hop, dial pinned to the validated IP; guard injectable for tests. **Tests:** table-driven — public URL passes; each blocked range class refused before dial; redirect from public into private refused mid-hop; `file://` rejected at validation; permissive-guard seam permits loopback.
- [x] 1.2 Entry fetcher (D2, D8): bounded single-document GET (`FETCH_TIMEOUT`, `UPLOAD_MAX_RAW_BYTES` cap), returns `Entry{Data, FinalURL}` where FinalURL reflects redirects; typed errors (blocked, unreachable/timeout, oversize, not-HTML); HTML sniffing moved from `upload.go` into the package; charset normalization to UTF-8 via `x/net/html/charset`. **Tests:** redirect updates FinalURL; oversize body → oversize error; JSON body → not-HTML error; ISO-8859-1 source comes back UTF-8; slow body hits timeout; pinned dial reaches the served IP.

## 2. Ingest pipeline — base URL and failure reasons

- [x] 2.1 Base-URL reference resolution (D4): `Process` accepts an optional base URL; `planIdentity` resolution order skip-class → absolute → pack-local → base-absolute → skip; `<base href>` overrides the base when present; resolved refs flow through existing classify/bake/keep. **Tests:** relative asset baked and rewritten to `/a/{slug}/external/host/path`; relative ref on allowlisted host recorded `kept-cdn`; `<base href>` wins over the source URL; `data:`/fragment refs untouched; absolute refs and pack uploads behave exactly as before.
- [x] 2.2 Relative refs inside baked external stylesheets resolve against the stylesheet's own URL (gap fix, D4): CSS jobs sourced from a URL get a full URL base dir, not path-only. **Tests:** `@font-face src: url(../fonts/a.woff2)` in a baked stylesheet is fetched, baked, and rewritten; unresolvable CSS ref lands `kept-external`.
- [x] 2.3 Retain the fetch-failure reason on kept-external plans and their manifest rows (D5) — in-memory result only, no schema change. **Tests:** 403 source → row status `kept-external` with reason `status 403`; timeout and oversize produce their reasons.

## 3. Asset baking adopts the guarded transport

- [x] 3.1 `ingest.Fetcher` performs requests through `internal/fetch`'s guarded transport (D3). **Tests:** existing fetch tests pass unchanged under the permissive guard; new case — uploaded HTML referencing an RFC1918 URL yields `kept-external` (upload still `201`), with no connection opened to the target.

## 4. Upload API — URL branch and strict gate

- [x] 4.1 URL branch in `POST /api/pages` (D1, D6): `url` XOR `file` (both → `422`, neither → `400`); entry fetch wired through `internal/fetch`; failure mapping malformed/blocked → `422`, unreachable/timeout/`5xx` → `502`, non-HTML → `415`, oversize → `413`, all before any storage. **Tests:** happy import returns `201` with identical response shape; each failure status asserted with nothing stored; both-fields and neither-field rejections.
- [x] 4.2 Strict completeness gate (D5): for URL imports, any `kept-external` in the manifest fails between `Process` and `putObjects` with `422 import_incomplete` — `unresolved[]` of source URL + reason, save-and-upload guidance; nothing stored, no page row. File uploads unchanged. **Tests:** one 403'd asset → `422` with correct body and `GET /api/pages/{slug}` → `404`; fully-resolved import → `201`; file upload with `kept-external` still `201`.

## 5. Admin UI

- [x] 5.1 Upload view gains a source-URL input (exactly one of URL/file per submission); renders `201` result link as today; on `import_incomplete` lists unresolved assets with the save-and-upload guidance; on `502`/`415`/`413`/`422` shows the API's error message; form state survives failures. **Tests:** covered by the e2e journeys in 6.1.

## 6. End-to-end

- [x] 6.1 Integration e2e (`internal/e2e`, testcontainers MinIO+Postgres, in-process `httptest` source with permissive guard): import journey — URL import → `201` → page serves at `/p/{slug}/` with assets at `/a/{slug}/…`; strict-failure journey — partially fetchable source → `422`, nothing served, nothing stored; regression journey — file upload unchanged. **Tests:** the journeys themselves are the acceptance check.

## 7. Release gate

- [x] 7.1 `gofmt -l .` empty; `go vet ./...` and `go vet -tags=integration ./...` clean; `go test ./...` and `go test -tags=integration ./...` green; README unchanged (no new config to document).
