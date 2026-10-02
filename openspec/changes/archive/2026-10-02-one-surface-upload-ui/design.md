## Context

The upload view in `internal/serve/static/index.html` (`renderUpload`)
currently renders the identifier, a URL input, and a file dropzone as three
independent stacked fields. Their exclusivity is enforced only by an error
message on submit (client) and a 422 (server, `internal/upload/upload.go`).
A throwaway prototype (`prototype-upload.html`, rooted at the repo) explored
three structural variants with the user; variant A — "stacked card" — won.

Constraints: the UI is one hand-written HTML file, no build step, no
framework, no npm (admin-ui spec). The API contract of `POST /api/pages`
(`url` XOR `file`, optional `identifier` defaulting to `page`) is frozen —
this change is UI-only. The slug scheme is `{sanitized-identifier}-{code}`
with code allocated per identifier starting at 1 (`internal/slug/slug.go`).

## Goals / Non-Goals

**Goals:**
- Make the URL-vs-file relationship (one source, not two fields) visible
  without explanation: one card, two layers, exactly one shown source.
- Replace the error-driven exclusivity (client "not both" message) with
  last-action-wins selection.
- Mandatory identifier in the UI with a live, honest URL preview.
- Copy the validated prototype's markup, styles, and behavior as-is.

**Non-Goals:**
- No server/Go changes; no API contract change; no dark theme; no changes
  to the list or detail views; no tooltips, auto-publish, or preview of
  page contents (see proposal Non-goals).

## Decisions

- **D1: One card, two layers (variant A).** The card is a bordered
  container: a `<label for="file">` drop layer on top, a CSS divider, and a
  URL input row as the card's footer. Alternatives considered: two
  side-by-side selectable cards (variant B of the discussion — more
  explicit but keeps the choice), full-bleed drop zone with floating URL
  pill (variant B of the prototype — visually winning but the user chose
  the stacked card), segmented tabs (heaviest, hides an option). The user
  picked variant A from the working prototype; copy markup from
  `prototype-upload.html` rather than re-deriving it.

- **D2: Last-action-wins, not disable, not error.** `setFile` replaces a
  set URL; a non-empty URL input replaces an attached file. The card always
  renders the single current source as a green summary chip (✓ name/size or
  ✓ URL, with a ✕ clear control). Alternatives considered: keep the "not
  both" error (punishes discovery of the form); mutually disable the other
  input (silent, surprising — a typed URL vanishing without a trace).
  Last-action-wins means the UI can never produce the state the server
  422s, and the summary chip makes the replacement visible, not silent.

- **D3: URL input is a DOM sibling of the file label, visually inside the
  card.** A text input nested inside a `<label for="file">` opens the file
  picker on click (labels forward clicks to their target) and is a native
  drop target for text — both traps are avoided structurally, not by
  event juggling. The drop handler covers the whole card container, so a
  file dropped on the URL input area still lands as the file source.

- **D4: Mandatory identifier is a UI gate only.** `requireIdent` blocks
  Publish with an inline error + focus when the sanitized identifier is
  empty or reserved; the server keeps accepting an omitted identifier
  (default `page`) so API consumers and scripts are unaffected. The
  client-side sanitizer mirrors `slug.Sanitize` (lowercase, `[^a-z0-9-]` →
  `-`, collapse/trim dashes, 64-char cap) and duplicates the reserved-word
  set; the server remains the validator.

- **D5: URL preview shows `/p/{sanitized}-1`.** The first publish for an
  identifier always gets code 1, so the preview is exact for first use; for
  an identifier published before, the UI cannot know the counter (it lives
  in SQLite) and shows `-1` anyway. Alternatives considered: query the next
  code from an API (no such endpoint; new surface for a cosmetic detail);
  omit the suffix (loses the concrete shape the prototype validated). The
  prototype's accepted behavior is kept as-is; the number is bookkeeping
  the success result corrects immediately after publish.

- **D6: Prototype disposal.** `prototype-upload.html` is deleted as the
  final task of this change; its markup lives on inside
  `index.html`. (Prototype skill: fold the validated decision, discard the
  artifact.)

## Risks / Trade-offs

- [Client sanitizer drifts from `slug.Sanitize`] → The server re-validates
  and returns 422 with its message; the drift window is cosmetic (preview
  slightly off), never corrupting. The preview is never authoritative.
- [Paste-on-page is less discoverable than a dedicated URL field] → The
  input is a real, visible, bordered field in the card footer — paste into
  it is ordinary typing; no hidden gesture is required.
- [Last-action-wins replaces user input without confirmation] → The
  replacement is shown immediately (summary chip), and the clear control
  undoes it; the replaced value was never submitted anywhere.
- [README screenshots go stale] → Refresh `screenshot-upload.png` in the
  same change.

## Migration Plan

UI-only; the single `index.html` ships inside the one binary. Deploy is a
normal rollout; rollback is a normal rollback. No data, schema, or API
migration.

## Open Questions

(none — design resolved against the working prototype)
