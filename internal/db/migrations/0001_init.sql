CREATE TABLE pages (
    slug        TEXT PRIMARY KEY,
    identifier  TEXT NOT NULL,
    code        INT  NOT NULL,
    asset_count INT  NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX pages_identifier_code_idx ON pages (identifier, code);

CREATE TABLE assets (
    slug         TEXT NOT NULL REFERENCES pages(slug) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    source_url   TEXT NOT NULL,
    content_type TEXT NOT NULL,
    bytes        BIGINT NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('local', 'baked', 'kept-cdn', 'kept-external')),
    PRIMARY KEY (slug, path)
);

CREATE TABLE counters (
    identifier TEXT PRIMARY KEY,
    next       INT NOT NULL DEFAULT 2
);
