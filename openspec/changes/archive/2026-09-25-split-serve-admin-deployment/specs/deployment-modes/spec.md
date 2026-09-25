# deployment-modes Delta

## ADDED Requirements

### Requirement: Mode selection
The system SHALL support a `SERVER_MODE` environment variable with values `serve`, `admin`, or `all` (default `all`). An invalid or unknown mode SHALL fail startup with a descriptive error. The default `all` mode SHALL preserve the current single-instance behavior: one process mounts and serves every plane.

#### Scenario: Default mode boots everything
- **WHEN** the service starts without `SERVER_MODE` set
- **THEN** it behaves as `all`: serving routes, admin API, upload UI, and health are all mounted

#### Scenario: Invalid mode fails fast
- **WHEN** the service starts with `SERVER_MODE=both`
- **THEN** startup fails with an error naming the valid modes, before opening any network listener

### Requirement: Per-mode required configuration
In `serve` mode the system SHALL require only storage configuration (`STORAGE_DRIVER` and its driver-specific settings) and SHALL NOT require `DATABASE_URL` or `AUTH_TOKEN`; it SHALL NOT open a database connection, run migrations, create the bucket, or run the lifecycle sweep. In `admin` and `all` modes the system SHALL require `DATABASE_URL` and `AUTH_TOKEN` in addition to storage configuration, and SHALL run migrations, bucket creation, and the lifecycle sweep at boot. Missing required configuration SHALL fail startup.

#### Scenario: Serve instance boots without any database configuration
- **WHEN** a serve-mode instance starts with storage settings but no `DATABASE_URL` and no `AUTH_TOKEN` in its environment
- **THEN** startup succeeds and no PostgreSQL connection is ever opened

#### Scenario: Admin instance fails fast without database configuration
- **WHEN** an admin-mode instance starts without `DATABASE_URL`
- **THEN** startup fails with an error naming the missing variable

### Requirement: Plane-scoped routing
A serve-mode instance SHALL mount only the page-serving routes (`/p/*`, `/a/*`, `/healthz`) and SHALL answer `404` for `/` and `/api/*`. An admin-mode instance SHALL mount only the upload UI (`/`), the admin API (`/api/*`, including park/unpark), and `/healthz`, and SHALL answer `404` for `/p/*` and `/a/*`. An `all`-mode instance SHALL mount the full surface as today.

#### Scenario: Serve instance does not expose the admin API
- **WHEN** `POST /api/pages` is sent to a serve-mode instance
- **THEN** the response is `404` and no upload or ingest occurs

#### Scenario: Admin instance does not serve pages
- **WHEN** `GET /p/some-slug-1/` is sent to an admin-mode instance
- **THEN** the response is `404`

### Requirement: Per-mode health probes
In `serve` mode, `GET /healthz` SHALL probe object storage (a `Stat` request): any storage response, including `ErrNotFound` for a missing probe key, SHALL report healthy; a storage transport failure SHALL report `503`. In `admin` and `all` modes, `/healthz` SHALL probe PostgreSQL as today. Serve-mode health MUST NOT depend on the database.

#### Scenario: Serve instance stays healthy while the database is down
- **WHEN** a serve-mode instance receives `GET /healthz` and PostgreSQL is unreachable (or unconfigured)
- **THEN** the response is `200` provided object storage responds

#### Scenario: Serve instance detects storage failure
- **WHEN** the storage endpoint refuses connections and `GET /healthz` is received by a serve-mode instance
- **THEN** the response is `503`

### Requirement: Concurrent admin boots migrate safely
`db.Migrate` SHALL serialize concurrent invocations with a PostgreSQL advisory lock so multiple admin replicas booting at once apply the schema exactly once and both start successfully.

#### Scenario: Two admin replicas migrate concurrently
- **WHEN** two admin instances run `Migrate` against the same database simultaneously
- **THEN** both succeed and the schema is fully applied
