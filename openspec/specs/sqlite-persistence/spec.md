# sqlite-persistence Specification

## Purpose
TBD - created by archiving change replace-postgres-with-sqlite. Update Purpose after archive.
## Requirements
### Requirement: SQLite is the write-side store
The write-side persistence (page rows, asset manifest, slug counters, lifecycle status) SHALL be a single SQLite database file in WAL mode, opened with `busy_timeout`, `foreign_keys=1`, and `synchronous=NORMAL`. The serve plane SHALL NOT open the database. The schema SHALL be created by the same ordered, version-tracked migration mechanism (a `schema_migrations` table), with each migration applied atomically.

#### Scenario: Fresh boot creates schema
- **WHEN** an admin-mode instance boots with `SQLITE_PATH` pointing at a nonexistent file
- **THEN** the database file is created, all migrations apply, and startup succeeds

#### Scenario: Serve mode never opens the database
- **WHEN** a serve-mode instance starts with no `SQLITE_PATH` in its environment
- **THEN** startup succeeds and no SQLite file is ever opened

### Requirement: Migrations are safe against a same-file concurrent boot
`db.Migrate` SHALL run inside a single `BEGIN IMMEDIATE` transaction so concurrent invocations against the same database file serialize on SQLite's write lock: the loser waits on `busy_timeout`, re-reads `schema_migrations`, and skips already-applied migrations. Both boots SHALL succeed with the schema applied exactly once.

#### Scenario: Two processes migrate the same file concurrently
- **WHEN** two admin instances boot against the same `SQLITE_PATH` simultaneously
- **THEN** both succeed and the schema is fully applied exactly once

### Requirement: Single admin writer
At most one admin instance SHALL write the database. Concurrent boots against the same file remain safe per the migration requirement; two different hosts MUST NOT write the same database simultaneously. Recovery to a new host SHALL be restore-based: restore the database from its replica, then start a single admin.

#### Scenario: Failover to a new host
- **WHEN** the admin host is lost and an operator restores the database from the replica on a new host and starts one admin instance
- **THEN** the new admin serves the API with the replicated bookkeeping state

### Requirement: WAL replication with an accepted convergence window
Deployment SHALL run a Litestream sidecar that continuously replicates the database WAL to the page bucket under the reserved `_db/` prefix. Because replication is asynchronous, a restored database MAY be behind the bucket by up to the replication interval: an object prefix may exist without its bookkeeping row, or a restored lifecycle status MAY disagree with object placement. The system SHALL converge when the corresponding operation is re-run (idempotent re-upload, re-park/unpark, re-delete), and this divergence SHALL be documented as an accepted compromise. Replica objects MUST NOT be publicly reachable: slugs cannot begin with `_`, and the edge maps only `/p/*` and `/a/*` into the bucket.

#### Scenario: Upload lost from the database still converges
- **WHEN** a crash loses a committed upload's rows but its objects are in the bucket, and the same upload is repeated after restore
- **THEN** the re-upload allocates a slug, writes its rows, and the page is fully managed again

#### Scenario: Replica objects are not publicly served
- **WHEN** a request targets `/p/_db/...` or any path whose first segment starts with `_`
- **THEN** no replica object is served (the identifier sanitization and reserved prefixes prevent such a slug from existing)

### Requirement: One-time migration from Postgres
A one-time command SHALL move existing rows (pages, assets, counters) from an existing Postgres instance into the SQLite database. Slug counters SHALL be copied exactly — codes never move backward.

#### Scenario: Row move preserves counters
- **WHEN** the migration command runs against a Postgres instance whose counter for identifier `x` is 7
- **THEN** the SQLite counter for `x` is 7 and the next allocated code is 7

