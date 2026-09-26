# Page

Page is self-serve hosting for static pages. Drop in an HTML file or a zip
exported from Figma or Framer — or saved straight from your browser — and
get a permanent URL on your own domain, in seconds, without engineering or
ops help.

## Quick start

Prerequisites:

- **Go** — runs the server.
- **Docker** — runs its two dependencies, started by `make up`:
  MinIO (S3-compatible object storage, API on :9000, console on :9001) and
  Postgres. The storage bucket is created automatically.

```bash
make up      # MinIO + Postgres via docker compose
make run     # server on :8080 (auth token: devtoken)
make seed    # uploads a sample Framer-style pack (no server needed)
open http://localhost:8080/p/sample-1/
```

## Manage pages

Open `http://localhost:8080/`, enter token `devtoken`, and you get the
management UI: every page with its lifecycle status, the full asset manifest
per page, park/unpark, and permanent delete. Upload lives there too —
optionally set an identifier and drop an `.html` file or a `.zip`:

![Web admin — manage pages](screenshot.png)

Or use the API:

```bash
# Upload (→ 201 {"slug":"landing-page-1","url":"/p/landing-page-1/",...})
curl -H "Authorization: Bearer devtoken" -F "file=@pack.zip" \
     -F "identifier=landing-page" http://localhost:8080/api/pages

# List pages (paginated: ?limit=&offset=&status=)
curl -H "Authorization: Bearer devtoken" http://localhost:8080/api/pages

# Page detail with the full asset manifest
curl -H "Authorization: Bearer devtoken" http://localhost:8080/api/pages/landing-page-1

# Take down (serves 404) / restore
curl -H "Authorization: Bearer devtoken" -X POST http://localhost:8080/api/pages/landing-page-1/park
curl -H "Authorization: Bearer devtoken" -X POST http://localhost:8080/api/pages/landing-page-1/unpark

# Delete permanently (objects + metadata; refuse while a transition runs)
curl -H "Authorization: Bearer devtoken" -X DELETE http://localhost:8080/api/pages/landing-page-1
```

The page is served at `/p/{slug}/`. Deleting a page frees its slug's storage
but never reuses its code — slug counters only move forward.

## Basic configuration

| Variable | Default | Notes |
|---|---|---|
| `SERVER_MODE` | `all` | `serve` pages/assets only · `admin` upload UI + API only · `all` everything |
| `DATABASE_URL` | `postgres://page:page@localhost:5432/page` | not needed in serve mode |
| `AUTH_TOKEN` | `devtoken` | bearer token for `/api/*`; not needed in serve mode |
| `STORAGE_DRIVER` | `s3compat` | or `mem` (tests) |
| `S3_ENDPOINT` | `localhost:9000` | required for s3compat |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | `minioadmin` | required for s3compat |
| `S3_BUCKET` | `pages` | |
| `S3_SECURE` | `false` | https to the endpoint |
| `S3_PATH_STYLE` | `true` | MinIO needs `true` |
| `ADDR` | `:8080` | |
| `HTML_CACHE_TTL` | `60s` | how fast parked pages stop serving |

Storage works with any S3-compatible endpoint (AWS S3, R2, B2, Spaces,
MinIO, …), configured entirely via env.

The full variable list (upload caps, ingest limits, keep-external CDNs) and
the production deployment guide are in [AGENTS.md](AGENTS.md).
