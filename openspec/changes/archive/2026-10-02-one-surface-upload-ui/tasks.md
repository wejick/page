## 1. Port the source card into the upload view

- [x] 1.1 Replace `renderUpload`'s markup and the upload-related CSS in
      `internal/serve/static/index.html` with the prototype's variant-A card,
      copied as-is: heading "Publish new page" (intro paragraph dropped),
      one bordered card with a `<label for="file">` drop layer, divider, and
      URL input row as a DOM sibling of the label (not nested inside it).
      Acceptance: the served `/` upload view renders the card; clicking the
      URL input focuses the field and does not open the file picker.
- [x] 1.2 Wire the shared source state — `setFile`/`setUrl`/clear with
      last-action-wins — and the whole-card drop handler from the prototype;
      keep the existing submit path (`FormData` to `POST /api/pages`), the
      client-side extension rejection, the result panel, and all error
      handling unchanged. Remove the "not both" client error (unreachable
      now). Acceptance: attaching a file replaces a typed URL and vice
      versa; the summary chip (✓ name+size or ✓ URL, ✕ clear) always shows
      the one source Publish will submit; dropping a file on the URL input
      area lands it as the file source.

## 2. Identifier gating and preview

- [x] 2.1 Add the client-side sanitizer (mirrors `slug.Sanitize`:
      lowercase, `[^a-z0-9-]` → `-`, collapsed/trimmed dashes, 64-char cap),
      the reserved-identifier set from `internal/slug`, the live
      `/p/{sanitized}-1` preview line, and `requireIdent` gating on Publish
      (inline error + focus, no request when empty or reserved). Acceptance:
      typing `My Cool Page!` previews `/p/my-cool-page-1`; `api` warns
      reserved; Publish with an empty identifier shows the inline error,
      focuses the field, and sends nothing.

## 3. Verify and clean up

- [x] 3.1 Browser pass over the full flow on a running instance: publish via
      file and via URL (201 result with asset counts), `import_incomplete`
      guidance, 502 cause surfacing, clear control, keyboard focus visible.
      Acceptance: every scenario in the delta spec's admin-ui requirements
      observed passing; `go test ./...` and
      `go test -tags=integration ./...` green (the file ships inside the
      binary, so the suite guards the serve path).
- [x] 3.2 Refresh `screenshot-upload.png` (the README shows the old upload
      view). Acceptance: screenshot shows the new card.
- [x] 3.3 Delete `prototype-upload.html`. Acceptance: repo no longer
      contains the prototype; the design it validated is captured in this
      change's design.md.
