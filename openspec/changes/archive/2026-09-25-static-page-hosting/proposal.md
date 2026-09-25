## Why

The internal team produces static pages from Figma/Framer exports or browser "Save As" packs and has no self-serve way to publish them at a stable URL — every publish is a manual server drop with hand-fixed links. We need a service where uploading an HTML pack returns a working URL immediately, with assets baked into our storage so pages outlive their source tool's CDN.

## What Changes

- New Go service: token-authenticated upload API + one hand-written HTML upload page; dumb byte-serving layer.
- Ingest pipeline: single HTML or zip → safe unzip → entry detection → asset reference scanning → best-effort baking into object storage, refs rewritten to origin-absolute paths.
- Incentive-based classification: refs to stable CDNs (Google Fonts, jsDelivr…) stay external by policy (`kept-cdn`); everything else bakes; fetch failures never block upload (`kept-external`, recorded in the manifest).
- Immutable pages: every upload creates a new page. Slugs are `{identifier}-{counter}`, stored as separate columns.
- Path-based serving on one origin (`/p/{slug}/`, `/a/{slug}/`): no wildcard DNS/TLS. In production a CDN fronts the bucket's byte paths; Go handles upload/ingest/API.
- Serve path is DB-free by construction: entry HTML normalized to `{slug}/index.html`, content types in object metadata, in-memory HTML cache.
- Vendor-neutral storage behind a 4-method `Storage` seam (s3compat + mem drivers); MinIO in dev, any S3-compatible store in prod. Postgres holds write-side metadata only.

## Non-Goals

- Page updates or versioning — new upload = new page; delete-and-recreate only.
- Custom domains, public sign-up, multi-tenancy.
- SSO integration — static bearer token suffices internally.
- Malware/phishing scanning — trusted internal uploaders.
- Async ingest or progress streaming — caps keep ingest synchronous.
- Runtime-JS URL rewriting — undetectable statically; asset dual-mount mitigates.

## Capabilities

### New Capabilities

- `page-upload`: Authenticated upload API + minimal ajax UI for HTML/zip packs, with validation and zip-safety checks.
- `ingest-pipeline`: Reference scanning, keep-external classification, bounded baking, URL rewriting, per-asset manifest.
- `slug-assignment`: Identifier sanitization, atomic per-identifier counters, reserved names, slug uniqueness.
- `page-serving`: DB-free byte serving, dual-mounted asset prefixes, immutable cache headers, in-memory HTML cache.
- `object-storage`: The 4-method storage seam with S3-compatible and in-memory drivers.

### Modified Capabilities

(none — greenfield)

## Impact

Greenfield codebase; no existing systems affected. Dependencies (each with a direct consumer): pgx/v5 (metadata), minio-go (storage), testcontainers-go (integration tests). External: IT grants one DNS name + one TLS cert. Operational surface: one bucket, one Postgres DB, one service.
