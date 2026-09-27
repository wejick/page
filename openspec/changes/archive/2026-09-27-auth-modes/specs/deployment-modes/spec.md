## MODIFIED Requirements

### Requirement: Per-mode required configuration
In `serve` mode the system SHALL require only storage configuration (`STORAGE_DRIVER` and its driver-specific settings) and SHALL NOT require `DATABASE_URL`, `AUTH_TOKEN`, or any auth-mode configuration; it SHALL NOT open a database connection, run migrations, create the bucket, or run the lifecycle sweep. In `admin` and `all` modes the system SHALL require `DATABASE_URL` in addition to storage configuration, and SHALL run migrations, bucket creation, and the lifecycle sweep at boot. Auth configuration SHALL be selected by `AUTH_MODE` (default `token`) and validated at startup: `token` requires `AUTH_TOKEN`; `none` MUST NOT be combined with `AUTH_TOKEN`; `oidc` requires `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, and `SESSION_SECRET`, and MAY combine them with `AUTH_TOKEN` as the machine path. Missing or conflicting required configuration SHALL fail startup with errors naming the variables.

#### Scenario: Serve instance boots without any database configuration
- **WHEN** a serve-mode instance starts with storage settings but no `DATABASE_URL` and no `AUTH_TOKEN` in its environment
- **THEN** startup succeeds and no PostgreSQL connection is ever opened

#### Scenario: Admin instance fails fast without database configuration
- **WHEN** an admin-mode instance starts without `DATABASE_URL`
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
