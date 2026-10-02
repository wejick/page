## MODIFIED Requirements

### Requirement: Per-mode required configuration
In `serve` mode the system SHALL require only storage configuration (`STORAGE_DRIVER` and its driver-specific settings) and SHALL NOT require `SQLITE_PATH`, `AUTH_TOKEN`, or any auth-mode configuration; it SHALL NOT open a database, run migrations, create the bucket, or run the lifecycle sweep. In `admin` and `all` modes the system SHALL require `SQLITE_PATH` in addition to storage configuration, and SHALL run migrations, bucket creation, and the lifecycle sweep at boot. Auth configuration SHALL be selected by `AUTH_MODE` (default `token`) and validated at startup: `token` requires `AUTH_TOKEN`; `none` MUST NOT be combined with `AUTH_TOKEN`; `oidc` requires `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, and `SESSION_SECRET`, and MAY combine them with `AUTH_TOKEN` as the machine path. Missing or conflicting required configuration SHALL fail startup with errors naming the variables.

#### Scenario: Serve instance boots without any database configuration
- **WHEN** a serve-mode instance starts with storage settings but no `SQLITE_PATH` and no `AUTH_TOKEN` in its environment
- **THEN** startup succeeds and no database is ever opened

#### Scenario: Admin instance fails fast without database configuration
- **WHEN** an admin-mode instance starts without `SQLITE_PATH`
- **THEN** startup fails with an error naming the missing variable

#### Scenario: Token mode fails without AUTH_TOKEN
- **WHEN** an admin-mode instance starts with `AUTH_MODE=token` and no `AUTH_TOKEN`
- **THEN** startup fails with an error naming `AUTH_TOKEN`

#### Scenario: None mode rejects a configured token
- **WHEN** an admin-mode instance starts with `AUTH_MODE=none` and `AUTH_TOKEN` set
- **THEN** startup fails with an error explaining `none` accepts no credentials

#### Scenario: Oidc mode fails without its variables
- **WHEN** an admin-mode instance starts with `AUTH_MODE=oidc` but no `SESSION_SECRET`
- **THEN** startup fails with an error naming the missing variable(s)

#### Scenario: Oidc mode accepts an optional machine token
- **WHEN** an admin-mode instance starts with `AUTH_MODE=oidc`, all OIDC variables, `SESSION_SECRET`, and `AUTH_TOKEN` set
- **THEN** startup succeeds (discovery and boot proceed against the configured IdP)

### Requirement: Per-mode health probes
In `serve` mode, `GET /healthz` SHALL probe object storage (a `Stat` request): any storage response, including `ErrNotFound` for a missing probe key, SHALL report healthy; a storage transport failure SHALL report `503`. In `admin` and `all` modes, `/healthz` SHALL probe the database (a trivial SQLite query). Serve-mode health MUST NOT depend on the database.

#### Scenario: Serve instance stays healthy while the database is down
- **WHEN** a serve-mode instance receives `GET /healthz` and no database is configured
- **THEN** the response is `200` provided object storage responds

#### Scenario: Serve instance detects storage failure
- **WHEN** the storage endpoint refuses connections and `GET /healthz` is received by a serve-mode instance
- **THEN** the response is `503`

#### Scenario: Admin instance detects database failure
- **WHEN** the SQLite database file is unreadable (missing, corrupt, or permission-denied) and `GET /healthz` is received by an admin-mode instance
- **THEN** the response is `503`

## REMOVED Requirements

### Requirement: Concurrent admin boots migrate safely
**Reason**: A shared file cannot serve concurrent writers; the advisory-lock guarantee this requirement described belonged to Postgres-as-a-shared-service.
**Migration**: Replaced by the single-writer contract below — at most one admin writer per database file, same-file concurrent boots still safe, failover restore-based via the Litestream replica.

## ADDED Requirements

### Requirement: Single admin writer replaces replica concurrency
The deployment SHALL run at most one admin writer per database file. Two admin processes booting against the same database file SHALL still both succeed (migrations serialize on SQLite's write lock and are applied exactly once). Running admin instances on two different hosts against one database file is a deployment error and MUST be prevented by orchestration, not by the application. Failover SHALL be restore-based: restore the database from its Litestream replica on a new host, then start one admin.

#### Scenario: Two admin processes boot against the same file
- **WHEN** two admin instances on the same host start simultaneously with the same `SQLITE_PATH`
- **THEN** both succeed and the schema is fully applied exactly once

#### Scenario: Documented single-writer contract
- **WHEN** an operator consults the deployment documentation
- **THEN** it states that exactly one admin writer is permitted and that failover is restore-based via the Litestream replica
