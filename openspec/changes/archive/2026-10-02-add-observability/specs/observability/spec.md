## ADDED Requirements

### Requirement: Structured logging to stdout
The system SHALL emit all logs as JSON via `log/slog` to stdout, with the
level controlled by `LOG_LEVEL` (default `info`; invalid values fail
configuration validation). The default handler and the legacy log package
SHALL NOT be used elsewhere in the binary.

#### Scenario: Logs are JSON on stdout
- **WHEN** any log line is emitted at or above the configured level
- **THEN** it is a single JSON object on stdout with `time`, `level`, `msg`,
  and any structured attributes

#### Scenario: Level knob suppresses debug
- **WHEN** `LOG_LEVEL=info` and a handler logs at debug (e.g. healthz access)
- **THEN** no line is emitted

### Requirement: Boot trail
Boot steps SHALL each log one `info` line on completion including its
duration (database open, migrate, bucket create, lifecycle sweep, OIDC
discovery), and startup SHALL log a summary line echoing mode, addr,
storage driver, auth mode, and cache TTL with secrets redacted. Boot
failures SHALL be logged once (as the fatal error), not per step.

#### Scenario: Successful admin boot leaves a trail
- **WHEN** an `all` instance boots cleanly
- **THEN** stdout contains a duration line per completed boot step and a
  final listening line echoing the running configuration

#### Scenario: Config summary redacts secrets
- **WHEN** the startup summary echoes the configuration
- **THEN** the auth token and session secret values do not appear

### Requirement: Request IDs on every request
Every HTTP request SHALL be assigned a request ID (random 128-bit hex,
honoring an incoming `X-Request-ID`), exposed to handlers, echoed in the
`X-Request-ID` response header, and included in access-log and error lines.

#### Scenario: Client-supplied request ID is honored
- **WHEN** a request arrives with header `X-Request-ID: abc123`
- **THEN** the response carries `X-Request-ID: abc123` and the access line
  logs that ID

### Requirement: Access log
The system SHALL log one `info` line per completed HTTP request (except
`GET /healthz`, which logs at debug) containing method, route pattern,
raw path, status, duration, bytes written, and request ID.

#### Scenario: Serving a page emits an access line
- **WHEN** `GET /p/{slug}/` completes with 200
- **THEN** one access line is logged with route pattern `GET /p/{slug}/{$}`,
  status 200, and the request duration

### Requirement: Panic recovery
A panic in any handler SHALL be recovered, logged at error with the stack,
request ID, and method+path, and answered with a 500 response rather than
dropping the connection.

#### Scenario: Panicking handler yields a 500
- **WHEN** a handler panics
- **THEN** the client receives HTTP 500 and the error log includes the
  panic value, stack, and request ID

### Requirement: Error logs carry request context
Server-side error lines SHALL include the request ID and route pattern in
addition to the error (from handlers such as `serveError` and
upload/lifecycle failures).

#### Scenario: Storage failure is attributable
- **WHEN** a serve-path `Get` returns a transport error
- **THEN** the 500's error line includes the request ID of the affected
  request

### Requirement: OTEL metrics via standard environment
The system SHALL record metrics through OpenTelemetry and export them OTLP
when `OTEL_EXPORTER_OTLP_ENDPOINT` is set, reading only standard `OTEL_*`
variables for exporter configuration. When the endpoint is unset the meter
provider SHALL have no readers — recording is inert and nothing is
exported — with request-path behavior identical to the enabled case, and
the provider SHALL be shut down on graceful shutdown so recorded metrics
are flushed.

#### Scenario: Endpoint set exports to collector
- **WHEN** `OTEL_EXPORTER_OTLP_ENDPOINT` points at a reachable collector
- **THEN** recorded metrics are periodically exported OTLP and the final
  interval is flushed on graceful shutdown

#### Scenario: Endpoint unset leaves serving unaffected
- **WHEN** no `OTEL_EXPORTER_OTLP_ENDPOINT` is configured
- **THEN** the instance boots, serves, and reports health identically, with
  no export attempts

### Requirement: Metric set and labels
The system SHALL record: HTTP request count and duration histogram (labels:
route pattern, status); storage op count and duration (labels: operation
`put|get|stat|copy|delete_prefix`, outcome `ok|not_found|error`); HTML cache
outcome count (`hit|miss|evict`); upload count and duration (labels: outcome
`created|rejected|failed`); and lifecycle op count (labels: operation,
outcome). Metric labels MUST use route patterns and fixed enumeration names
only — never slugs, raw paths, or other unbounded values.

#### Scenario: Route labels stay bounded
- **WHEN** requests arrive for any number of distinct slugs
- **THEN** the HTTP metrics' label set remains bounded by the finite set of
  route patterns

#### Scenario: Storage outcomes distinguish misses from errors
- **WHEN** a `Get` returns `ErrNotFound` versus a transport error
- **THEN** the storage counter records outcome `not_found` versus `error`

### Requirement: Serving health is independent of the collector
Exporter failures MUST NOT affect request handling, `/healthz` responses,
or boot success, and MUST NOT log above `warn`.

#### Scenario: Collector unreachable during traffic
- **WHEN** the collector endpoint becomes unreachable while serving
- **THEN** requests continue to be served and `/healthz` keeps reporting
  healthy; export retries silently

### Requirement: Health response identifies the build
`GET /healthz` SHALL include `version` and `uptime` in its response body
alongside the existing healthy/unhealthy semantics (status code unchanged).

#### Scenario: Healthy response includes build info
- **WHEN** `/healthz` is called on a healthy instance
- **THEN** the body is JSON including `version` and `uptime` with status 200

### Requirement: Significant admin events emit one line
A completed upload SHALL log one `info` line with slug, duration, total
bytes, and asset counts per manifest status; a completed lifecycle
operation (park, unpark, delete) SHALL log one `info` line with slug, op,
and duration.

#### Scenario: Upload success is visible
- **WHEN** a zip pack uploads successfully
- **THEN** one line logs the assigned slug, ingest duration, total bytes,
  and per-status asset counts

#### Scenario: Park completes with a line
- **WHEN** an authenticated park request completes
- **THEN** one line logs the slug and op duration
