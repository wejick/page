# slug-assignment Specification

## Purpose
{identifier}-{counter} slug scheme: sanitization, atomic per-identifier counters, reserved names, uniqueness.

## Requirements

### Requirement: Slug format and counter allocation
Slugs SHALL have the form `{identifier}-{code}`, where identifier is user-supplied (or defaulted) and code is a per-identifier integer counter starting at 1. Codes SHALL be allocated atomically so concurrent uploads with the same identifier receive distinct codes; `pages.slug` SHALL be unique with a unique index as backstop.

#### Scenario: First upload of an identifier
- **WHEN** a page is uploaded with identifier `landing-page` for the first time
- **THEN** the slug is `landing-page-1`

#### Scenario: Subsequent uploads increment
- **WHEN** `landing-page-1` exists and another page is uploaded with identifier `landing-page`
- **THEN** the slug is `landing-page-2`

#### Scenario: Independent counters per identifier
- **WHEN** `landing-page-1` exists and a page is uploaded with identifier `pricing`
- **THEN** the slug is `pricing-1`

#### Scenario: Concurrent uploads same identifier
- **WHEN** two uploads with the same new identifier race
- **THEN** both succeed with distinct codes and neither is lost

### Requirement: Identifier sanitization
Identifiers SHALL be normalized to lowercase, restricted to `[a-z0-9-]`, with trimmed/collapsed dashes and a maximum length; invalid input (empty after sanitization) SHALL be rejected with `422`.

#### Scenario: Messy identifier normalized
- **WHEN** a user submits identifier `  Landing Page! `
- **THEN** the stored identifier is `landing-page` and the slug is `landing-page-1`

#### Scenario: Unsalvageable identifier rejected
- **WHEN** a user submits an identifier that sanitizes to empty (e.g. `???`)
- **THEN** the API returns `422`

### Requirement: Reserved identifiers
The system SHALL reject reserved identifiers (`api`, `a`, `p`, `ui`, `www`, `assets`, `cdn`, `static`, `healthz`) with `422`, because they would collide with routing prefixes.

#### Scenario: Reserved identifier rejected
- **WHEN** a user submits identifier `api`
- **THEN** the API returns `422` and no page is created

### Requirement: Slug parts stored separately
The system SHALL store identifier and code as distinct columns in addition to the composed slug, and MUST NOT derive the parts by parsing the slug at serve time (e.g. `landing-page-1-2` is a valid opaque identifier with its own counter).

#### Scenario: Numbered identifier with appended code
- **WHEN** a user uploads with identifier `landing-page-1`
- **THEN** the slug is `landing-page-1-1` and later uploads yield `landing-page-1-2`, unambiguously
