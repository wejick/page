# object-storage Delta

## MODIFIED Requirements

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
