## 1. Configuration and logging foundation

- [x] 1.1 Add `LOG_LEVEL` (default `info`, invalid values fail validation) and OTEL passthrough (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`) to `internal/config`, with config tests covering defaults, valid, and invalid values
- [x] 1.2 Build the root `slog` JSON handler on stdout in `cmd/server` (level from config, logger threaded explicitly via `serve.Options`/`upload.Options`/`lifecycle`); test that a handler over a buffer receives JSON with `time`/`level`/`msg` and that debug lines are suppressed at info level
- [x] 1.3 Replace the global default-handler call sites (serve, upload, lifecycle, auth/oidc) with the injected logger, normalizing attributes (`err`, `what`, `slug`, `op`, `route`); test: `grep`-level check that no non-slog log package usage remains, and one captured-output test per converted package

## 2. Boot trail

- [x] 2.1 Log one duration line per boot step (db open, migrate, bucket create, sweep, OIDC discovery) and the startup summary (mode, addr, driver, auth mode, cache TTL, bucket) with secrets redacted; test: boot an `all` instance against mem storage + temp SQLite and assert the step lines and absence of token/secret values in captured output

## 3. HTTP middleware

- [x] 3.1 Add middleware (recover → request-id → access log) wrapping the `serve.New` mux: 128-bit random hex request ID honoring incoming `X-Request-ID`, echoed response header; test: table-driven — supplied ID honored, unsupplied ID generated, header echoed, healthz logs at debug
- [x] 3.2 Panic recovery: recover, log stack + request ID + method/path, respond 500; test: a panicking test handler returns 500 and the captured log contains the panic value and request ID
- [x] 3.3 Access log line per request (method, route pattern, raw path, status, duration, bytes, request ID) with healthz at debug; test: exercise the mem-driver handler for a 200 page request and a 404 and assert fields in captured JSON
- [x] 3.4 Thread request ID + route pattern into `serveError`/`h.fail` error lines; test: force a storage error in a test store and assert the error line carries the request ID

## 4. Route-labeled instrumentation

- [x] 4.1 Add the registration helper in `serve.New` that wraps each handler with its pattern (one function over `mux.Handle`, no routing changes); test: pattern is visible to the wrapped metrics middleware for representative routes (`GET /p/{slug}/{$}`, `GET /a/{slug}/{rest...}`, `POST /api/pages`)

## 5. Metrics wiring

- [x] 5.1 Set up the noop/OTLP `*metric.MeterProvider` in `cmd/server` from `OTEL_*` env (noop when endpoint unset), pass via `serve.Options`, and shut down on graceful exit; test: noop path boots and serves identically with no export attempts; OTLP path against a local test collector (in-memory/exporter) flushes on shutdown
- [x] 5.2 Add the concrete instrumenting storage decorator (op × outcome counters, duration histograms; outcome `ok|not_found|error`) and wrap the driver at boot; test: run the existing `storagetest` conformance suite through the decorator over `mem`, and assert not_found vs error outcomes are distinguished
- [x] 5.3 Record HTTP request count + duration by route pattern and status in the middleware; test: in-memory reader (manual reader) — requests to distinct slugs produce labels bounded to route patterns, statuses recorded
- [x] 5.4 Record HTML cache hit/miss/evict counters in `internal/serve/cache.go`; test: repeated `pageIndex` requests record miss then hit, and an evicted entry records evict
- [x] 5.5 Record upload outcome (`created|rejected|failed`) + duration and the one-line upload success event (slug, duration, bytes, per-status asset counts); test: successful upload, rejected import, and failed ingest each produce the expected metric outcome and (for success) the structured line
- [x] 5.6 Record lifecycle op × outcome counters and the per-op completion line; test: park/unpark/delete via the lifecycle API over mem storage + temp SQLite record outcomes and emit lines
- [x] 5.7 Enrich `/healthz` with `version` and `uptime` JSON while keeping status-code semantics unchanged; test: healthy probe returns 200 with both fields; failing probe still 503

## 6. Verification

- [x] 6.1 Full gate: `gofmt -l .` empty, `go vet ./...` and `go vet -tags=integration ./...` clean, `go test ./...` and `go test -tags=integration ./...` green
- [x] 6.2 End-to-end sanity: boot `all` against MinIO (testcontainers) with `OTEL_EXPORTER_OTLP_ENDPOINT` unset and set — identical upload/serve/park behavior, stdout JSON trail present, no serving regression (e2e test in `internal/e2e`)
