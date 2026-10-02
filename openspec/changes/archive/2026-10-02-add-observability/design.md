## Context

The process today emits ~18 `slog` calls through the global default handler
(no JSON option, no level knob), has no middleware at all — `serve.New`
returns a bare `http.ServeMux` — and no metrics of any kind. Boot steps
(database open/migrate, bucket create, sweep, OIDC discovery) are silent on
success; the upload happy path emits nothing. The deployment will run an
OpenTelemetry collector; log delivery consumes stdout.

Constraints that shape this design:

- **The serve path never touches the database** — observability must not
  introduce a database reference into serve mode, and serving health/latency
  must never depend on the collector.
- **One seam only** (`internal/storage`); no new interfaces, DI, or
  indirection. Code is flat and concrete.
- **No config knob without a scenario**; no dependency without a direct
  consumer.

## Goals / Non-Goals

**Goals:**

- Every request visible: one structured access-log line with request ID,
  status, duration, bytes.
- Every boot step and every significant admin event (upload, lifecycle op)
  emits one structured line.
- OTLP metrics for HTTP, the storage seam, the HTML cache, uploads, and
  lifecycle ops, consumable by a standard collector.
- Serving behavior bit-for-bit unchanged when no collector is configured.

**Non-Goals:** traces/spans, runtime/process metrics, in-app log shipping,
dashboards/alerts, Litestream replication visibility (see proposal).

## Decisions

- **D1 — Logging: stdlib `slog` JSON handler on stdout, built once in
  `cmd/server/main.go`; `LOG_LEVEL` is the only knob (default `info`).**
  JSON-always because log delivery is external and consumers parse, not
  read; a `LOG_FORMAT` knob would serve developer taste, not a scenario.
  The logger is passed explicitly (field on `serve.Options`,
  `upload.Options`, `lifecycle.New`, …) rather than kept global, so tests
  capture output via a handler over a buffer.
  *Alternatives considered:* `LOG_FORMAT` knob (no consumer for text in
  prod); keep the global logger (untestable, drifts back to ad-hoc lines);
  zap/zerolog (dependency without a consumer `slog` doesn't meet).

- **D2 — Boot trail: each boot step logs one `info` line with duration;
  a startup summary echoes mode, addr, driver, auth mode, cache TTL, and
  storage bucket with secrets redacted.** Failure paths keep returning
  errors (logged once at the top of `main`), so no step logs its own
  failure and retries it.
  *Alternatives considered:* log only on failure (silent-when-healthy is
  the problem being fixed); config echo via a debug endpoint (another
  surface to secure for no gain).

- **D3 — Middleware chain `recover → request-id → access log` wraps the
  mux returned by `serve.New`; request IDs are 128-bit random hex, echoed
  in `X-Request-ID` if the client sent one, and included in every access
  line and in `serveError`/`h.fail` error lines.** `healthz` requests log
  at debug. Panic recovery responds 500 instead of net/http's default
  connection kill, logging the stack at error.
  *Alternatives considered:* otelhttp handler middleware (pulls in trace
  machinery we've explicitly excluded; route labels need our registration
  helper anyway); gorilla/middleware (dependency against stdlib-first).

- **D4 — Route labels come from a registration helper in `serve.New` that
  wraps each handler with its pattern; metric labels are always the
  pattern (`GET /p/{slug}`), never the raw path or slug.** Slugs are
  unbounded — as labels they would melt the collector; raw paths stay in
  access logs where cardinality is free.
  *Alternatives considered:* label by plane (`serve`/`admin`) only — loses
  per-route error rates we explicitly want; expose pattern via context
  from Go 1.22 ServeMux (not exposed to wrappers — this is why the helper
  exists).

- **D5 — Metrics: OpenTelemetry metrics only.** `go.opentelemetry.io/otel`
  + `otel/sdk` + `otlpmetrichttp` exporter, configured from the standard
  `OTEL_*` env vars (`OTEL_EXPORTER_OTLP_ENDPOINT`,
  `OTEL_EXPORTER_OTLP_PROTOCOL`); components receive an
  `otel/metric.Meter` (obtained once from the provider in `main`) through
  their `Options`. Endpoint unset → a reader-less provider, whose meters
  behave as noop; a broken meter degrades instruments to noop rather than
  failing the boot. A `PeriodicReader` exports in the background;
  `provider.Shutdown` runs in the graceful-shutdown path so the last
  interval isn't dropped.
  Serves the deployment requirement that a collector-based operator can
  see request rate/error/latency.
  *Alternatives considered:* hand-rolled Prometheus text exposition
  (another bespoke format to own; OTEL chosen by the deployment); expvar
  (nothing scrapes it); Prometheus client_golang (second metrics API for
  the same job); passing the whole `*metric.MeterProvider` through every
  Options (wider surface than the one handle components actually need).

- **D6 — Storage instrumentation is a concrete decorator: one struct
  implementing `storage.Storage` that wraps the real driver at boot in
  `main.go`, adding op counter + duration histogram per operation and
  outcome (`ok`/`not_found`/`error`).** No new interface — the interface
  already exists with two real drivers; the decorator is a single type in
  `internal/storage` (or its own small package) tested by wrapping `mem`
  in the existing conformance suite.
  *Alternatives considered:* instrument inside both drivers (duplicated
  across `mem`/`s3compat`); instrument at call sites (scattered, easy to
  miss future call sites).

- **D7 — Cache/upload/lifecycle counters live in their existing packages
  as plain `otel.Counter`/`histogram` fields created from a meter passed
  in `Options`.** HTML cache records hit/miss/evict; upload records
  outcome (`created`/`rejected`/`failed`) + duration; lifecycle records
  op × outcome. No new event bus or hooks.
  *Alternatives considered:* central metrics package that everything
  imports (a second global seam for no benefit); log-derived metrics
  (fragile, high cardininality parsing).

- **D8 — Collector independence invariant: exporter failures never log
  louder than `warn` at boot, never touch the request path (OTEL's SDK
  exports from its own goroutine), and never affect `/healthz`.** Serving
  starts with metrics enabled or noop — identical behavior.
  *Alternatives considered:* healthz reporting collector connectivity
  (couples serving health to a non-serving dependency, violating the
  serve-plane invariant).

## Risks / Trade-offs

- [OTLP/protobuf dependency tree lands in go.mod] → Metrics-only SDK is
  still the smallest standard path to the collector the deployment runs;
  vendor-pinned, no transitive HTTP frameworks.
- [JSON logs are harder to eyeball locally] → `jq`/docker tooling; a
  `LOG_FORMAT` knob was rejected as taste, not need (D1).
- [Access logs double log volume on busy serve instances] → `healthz` at
  debug; CDN terminates most edge traffic — origin serve logs can be
  sampled by delivery, and volume is the price of origin visibility.
- [Route pattern helper must not become a mini framework] → it is one
  function wrapping `mux.Handle` with a labeled handler; no routing
  behavior changes.
- [Metric cardinality regressions] → pattern/op-name label rule is
  normative in the spec; code review checklist item.

## Migration Plan

Purely additive: deploy the new image, point `OTEL_EXPORTER_OTLP_ENDPOINT`
at the collector. No schema, storage, or API changes; rollback is redeploy
of the previous image. Metrics appear in the collector on the next
scrape/push interval after boot.

## Open Questions

None — deployment shape (collector endpoint, log delivery) is external
configuration.
