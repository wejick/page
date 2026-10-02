## Why

The service is nearly silent: boot steps log nothing on success, there is no
access logging, no request IDs, no latency data, and the happy paths of the
smartest subsystems (upload/ingest, lifecycle) emit nothing. The only signals
today are `/healthz` and ~18 context-free error lines. Operations cannot
answer "what is this instance doing" from the process.

## What Changes

- Structured logging: one `slog` JSON handler on **stdout**, built once in
  `cmd/server`, with a `LOG_LEVEL` knob (default `info`). All existing call
  sites move onto consistent attribute conventions.
- Boot trails: each boot step (database open/migrate, bucket create, sweep,
  OIDC discovery) logs completion with duration; a startup line echoes the
  running configuration with secrets redacted.
- HTTP middleware: panic recovery, request IDs, and a one-line access log
  (method, route pattern, path, status, duration, bytes, request ID) per
  request; `healthz` logs at debug to stay quiet.
- Metrics via OpenTelemetry (metrics only, no traces): HTTP request counts
  and duration histograms labeled by route **pattern**, storage-seam op
  counts/durations, HTML cache hit/miss/evict, upload and lifecycle
  outcomes. Exported OTLP per standard `OTEL_*` env vars; unset endpoint →
  noop provider, zero overhead, serving unaffected.
- `GET /healthz` gains `version` and `uptime` in its response body.

## Capabilities

### New Capabilities
- `observability`: structured stdout logging, request IDs, access/error
  logging, OTEL metrics (HTTP, storage, cache, upload, lifecycle), and the
  invariant that serving health and latency never depend on the collector.

### Modified Capabilities

## Impact

- **Code**: `cmd/server/main.go` (logging/metrics setup, shutdown flush),
  new middleware in `internal/serve`, a concrete instrumenting decorator
  around `storage.Storage`, instrumentation in `upload`/`lifecycle`/`serve`,
  `internal/config` gains `LOG_LEVEL` + OTEL endpoint passthrough.
- **Dependencies**: `go.opentelemetry.io/otel`, `otel/sdk`, and an OTLP
  metrics exporter — justified by the collector-based deployment.
- **Deployment**: `OTEL_EXPORTER_OTLP_ENDPOINT` points at the collector;
  log delivery consumes stdout JSON. Serve-mode instances get the same
  treatment (no database references).
- **Cardinality contract**: metric labels use route patterns and op names
  only — never slugs or raw paths.

## Non-goals

- **No traces/spans.** Metrics only; trace IDs are not stamped into logs.
- **No runtime/process metrics** (memory, GC, goroutines) — no consumer yet.
- **No logging/metrics UI, dashboards, or alert rules** — collector-side.
- **No Litestream/replication observability in-app** — sidecar health stays
  an orchestration concern, documented rather than measured here.
- **No log shipping inside the binary** — stdout only; delivery is external.
- **No auth failures metric or brute-force detection** — visible via access
  logs at info level; hardening is a separate concern.
