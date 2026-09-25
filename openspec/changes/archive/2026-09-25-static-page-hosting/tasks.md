## 1. Scaffold & local environment

- [x] 1.1 Go module + flat layout (`cmd/server`, `internal/{storage,slug,ingest,upload,serve}`); gofmt/go vet clean. Tests: build + vet gate in CI. Acceptance: `go build ./... && go vet ./...` green
- [x] 1.2 `docker-compose.yml`: MinIO (:9000/:9001) + Postgres + `mc` bucket-init container. Acceptance: `docker compose up` yields working MinIO console and DB
- [x] 1.3 Migrations (write-side schema: `pages`, `assets`, `counters`; unique index on `pages.slug`). Tests: testcontainers migrate-up against real Postgres, constraint violations asserted. Acceptance: duplicate slug insert fails, counters table allocates
- [x] 1.4 Env config loader (driver/endpoint/creds/bucket/path-style, token, caps, allowlist). Tests: table-driven parse tests (defaults, missing-required errors). Acceptance: every env var in the README matrix parses
- [x] 1.5 Server bootstrap + `/healthz` wired to compose. Acceptance: `curl /healthz` green against compose stack

## 2. Storage seam

- [x] 2.1 `Storage` interface (`Put`/`Get`/`Stat`/`DeletePrefix`) + `mem` driver. Tests: table-driven unit tests — roundtrip, content-type persistence, DeletePrefix scoping. Acceptance: spec `object-storage` scenarios pass
- [x] 2.2 `s3compat` driver (minio-go, env-driven). Tests: testcontainers integration vs real MinIO — roundtrip incl. content type, missing key → typed not-found error. Acceptance: same suite passes with driver swapped by env only

## 3. Slug assignment

- [x] 3.1 Sanitizer + reserved-identifier denylist. Tests: table-driven — spec scenarios (`"  Landing Page! "` → `landing-page`, `???` → 422, `api` → 422). Acceptance: all slug-assignment sanitize/reserved scenarios green
- [x] 3.2 Atomic counter allocation (upsert `RETURNING`) + slug composition + retry-on-unique-conflict. Tests: integration with N concurrent goroutines on real Postgres. Acceptance: N concurrent uploads of one identifier → N distinct sequential codes

## 4. Serving layer (tracer bullet)

- [x] 4.1 Router (stdlib `net/http` 1.22 patterns): `/`, `/api/*`, `/p/{slug}`, `/p/{slug}/*`, `/a/{slug}/*` — both asset prefixes map to `{slug}/` keys. Tests: route-table httptest. Acceptance: every route resolves to the documented handler
- [x] 4.2 Byte serving: URL→key arithmetic (no DB), content types from object metadata, `nosniff`, immutable `Cache-Control` + ETag/304, slashless redirect, 404s. Tests: httptest suite with counting storage wrapper. Acceptance: page-serving scenarios green; DB receives zero queries
- [x] 4.3 `htmlCache` (lazy-fill, byte-budget LRU). Tests: counting wrapper — second request performs no additional `Get`; freshly uploaded page serves immediately (no stale negative state). Acceptance: in-memory cache scenarios green
- [x] 4.4 `make seed` (sample Framer-style pack into local MinIO). Acceptance: seeded page renders fully in browser via compose

## 5. Upload API & UI

- [x] 5.1 Bearer-token middleware on `/api/*`. Tests: httptest 401/200 paths. Acceptance: unauthenticated POST → 401, nothing stored
- [x] 5.2 Multipart handling + caps + zip-safety validation (traversal, absolute paths, symlinks, nested zips, entry count, decompressed cap). Tests: table-driven fixture packs, one per rejection. Acceptance: every bad pack → 4xx with message, zero objects stored
- [x] 5.3 Entry detection + normalization to `{slug}/index.html`. Tests: fixture packs — root index, shallowest fallback, nested entry, no-HTML. Acceptance: ingest-pipeline entry scenarios green
- [x] 5.4 Persistence: page row, manifest rows (`status=local`), `GET /api/pages/{slug}`. Tests: integration vs real Postgres + MinIO. Acceptance: manifest lists every local asset with correct status
- [x] 5.5 Upload page (hand-written HTML, fetch + FormData; no build step). Acceptance: e2e smoke via compose — drop pack → result URL renders the page

## 6. Ingest pipeline (baking)

- [x] 6.1 Reference scanner: HTML `src`/`href`/`srcset`/`poster`/style attrs/`<style>`/SVG `image`+`use`; CSS `url()`/`@import`/`@font-face`, recursive. Tests: table-driven fixture files per ref type. Acceptance: every ingest-pipeline scanning scenario found
- [x] 6.2 Classifier: signed-query-params → bake; config allowlist (fonts/js/icons/analytics) → `kept-cdn`; default bake. Tests: table-driven per outcome. Acceptance: all four classification outcomes proven, allowlist read from config only
- [x] 6.3 Bounded fetcher: per-asset cap, timeout, total budget, concurrency pool, byte-sniffed content types. Tests: `httptest.Server` fixtures (slow, oversize, 403); no live network. Acceptance: caps enforced; failures yield `kept-external`, never an upload error
- [x] 6.4 Rewriter: resolved refs → origin-absolute `/a/{slug}/...` in HTML + CSS; `http→https` upgrade on kept refs. Tests: golden-file comparisons. Acceptance: rewritten outputs byte-match goldens
- [x] 6.5 Wire pipeline into upload (single-HTML and zip paths). Tests: integration — Google Fonts page (no gstatic fetch attempted), unreachable asset → still 201. Acceptance: manifest shows `local`, `baked`, `kept-cdn`, `kept-external` all correct

## 7. End-to-end & polish

- [x] 7.1 E2E integration suite: Framer-style zip → serve `/p/{slug}/` → assets resolve via both `/a/` and `/p/`, slashless redirect, 404 on missing slug. Acceptance: green in CI (testcontainers, no live network)
- [x] 7.2 Make targets: `up`, `seed`, `test`, `test-integration`, `migrate`. Acceptance: each target runs standalone from a clean checkout
- [x] 7.3 README: run book, dev/prod env matrix, IT ask (one name + one cert), CDN config (behaviors, edge path-rewrite, missing-key 404 mapping incl. S3 403 quirk). Acceptance: a colleague goes compose-up → upload → view using only the README
