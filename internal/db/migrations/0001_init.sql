-- Initial schema (SQLite). Collapses the historical Postgres migration set
-- (0001 init + 0002 park status + 0003 delete status): SQLite stores start
-- fresh — rows from an existing Postgres deployment move over via
-- cmd/pgmigrate, never by replaying migration history.
CREATE TABLE pages (
    slug        TEXT PRIMARY KEY,
    identifier  TEXT NOT NULL,
    code        INT  NOT NULL,
    asset_count INT  NOT NULL DEFAULT 0,
    total_bytes INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT (CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    status      TEXT NOT NULL DEFAULT 'live'
        CHECK (status IN ('live', 'parking', 'parked', 'unparking', 'deleting'))
);

CREATE UNIQUE INDEX pages_identifier_code_idx ON pages (identifier, code);

CREATE TABLE assets (
    slug         TEXT NOT NULL REFERENCES pages(slug) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    source_url   TEXT NOT NULL,
    content_type TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('local', 'baked', 'kept-cdn', 'kept-external')),
    PRIMARY KEY (slug, path)
);

CREATE TABLE counters (
    identifier TEXT PRIMARY KEY,
    next       INT NOT NULL DEFAULT 2
);
