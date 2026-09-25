# object-storage Specification

## Purpose
Vendor-neutral storage seam: five operations including prefix-based Copy, with S3-compatible (minio-go) and in-memory drivers.

## Requirements

### Requirement: Vendor-neutral storage seam
All storage access (upload layer, ingest pipeline, serving layer, lifecycle layer) SHALL go through a five-operation interface — `Put`, `Get`, `Stat`, `Copy`, `DeletePrefix` — and this SHALL be the only repository-style interface in the codebase; no component may reference a storage SDK directly. The interface MUST NOT grow beyond these operations (no listing, multipart, or presigned URLs) unless a requirement demands it. `Copy` SHALL be prefix-based — `Copy(srcPrefix, dstPrefix)` copies every object stored under `srcPrefix` to the corresponding key under `dstPrefix` (mirroring `DeletePrefix`'s prefix semantics and hiding enumeration inside the driver), preserving each object's bytes, content type, and ETag.

#### Scenario: Driver swap without call-site changes
- **WHEN** the storage driver is switched from `mem` to `s3compat` via environment configuration
- **THEN** the upload → ingest → serve integration suite passes unchanged

#### Scenario: Prefix copy preserves every object
- **WHEN** objects `a/index.html` and `a/img.png` exist and `Copy("a/", "b/")` is called
- **THEN** `Get("b/index.html")` and `Get("b/img.png")` return identical bytes, content types, and ETags, and the source objects under `a/` are unchanged

#### Scenario: Copy overwrites existing destination keys
- **WHEN** `Copy(srcPrefix, dstPrefix)` targets a destination prefix that already holds objects
- **THEN** those destination keys are replaced with the copied objects (last-writer semantics), leaving no duplicates

### Requirement: S3-compatible driver
The system SHALL provide an `s3compat` driver built on minio-go that speaks the S3 wire protocol to any S3-compatible endpoint (AWS S3, MinIO, Cloudflare R2, Backblaze B2, etc.), configured entirely via environment (endpoint, credentials, bucket, path-style flag).

#### Scenario: Local MinIO roundtrip
- **WHEN** the service runs against MinIO in docker compose and a page is uploaded then fetched
- **THEN** the bytes, content type, and size returned by `Get` match what was stored

#### Scenario: Alternate vendor endpoint
- **WHEN** the driver is pointed at a non-AWS S3-compatible endpoint via env
- **THEN** upload and retrieval succeed without code changes

### Requirement: In-memory driver for unit tests
The system SHALL provide a `mem` driver so unit tests exercise the storage seam as a real implementation (not a mock of the interface) without Docker; integration tests use real MinIO via testcontainers.

#### Scenario: Unit test roundtrip
- **WHEN** a unit test performs Put then Get on the `mem` driver
- **THEN** the stored object is returned with its content type and size

### Requirement: Content type persisted per object
The `Put` operation SHALL persist the supplied content type with the object, and `Get`/`Stat` SHALL return it — content types are metadata of the object, not re-derived from file extensions at serve time.

#### Scenario: Content type survives roundtrip
- **WHEN** an object is stored with content type `text/css`
- **THEN** `Get` and `Stat` report `text/css` regardless of the key's file extension
