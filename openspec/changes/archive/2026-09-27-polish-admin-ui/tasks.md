## 1. Theme foundation

- [x] 1.1 Pin light scheme (D1): `color-scheme: light`, explicit body
      background/color. Introduce `:root` palette variables for accent,
      statuses, error, warning, muted (D2); replace scattered hex and bare
      `gray`; muted value meets WCAG AA on white. Acceptance: page renders
      identically in light browsers; no hardcoded one-off colors left in
      rules that the variables cover.
- [x] 1.2 Link and interaction polish (D10): `color: inherit` on the title
      link, one accent color for all links, button hover/active, row hover,
      `:focus-visible` rings. Acceptance: browser check — no default
      blue/purple link colors; keyboard tabbing shows visible focus; rows
      highlight on hover.

## 2. Ingest outcome surfacing

- [x] 2.1 Manifest presentation (D3, D4): badge styles for `kept-external`
      (warning) and `kept-cdn` (neutral), em dash instead of `0 B` for
      unstored assets, manifest column header renamed `Storage`. Acceptance:
      detail view mocked with all four asset statuses shows the right
      treatments.
- [x] 2.2 Kept-external footnote (D3): one explanatory line under the
      manifest table, rendered only when at least one asset is
      `kept-external`. Acceptance: toggling the status in a mock adds/removes
      the note.
- [x] 2.3 Upload ingest summary (D8): render the 201 response's `assets`
      counts in the success box with the existing details link; amber
      treatment when kept-external > 0. Acceptance: mocked success response
      with mixed counts renders the summary; form state intact afterwards.

## 3. Token chip

- [x] 3.1 Collapsible token entry (D5): header chip showing token state;
      click toggles the input row; 401 auto-expands the row with the error
      message; localStorage persistence and re-route-on-input unchanged.
      Acceptance: browser check — stored token collapses to chip; clearing
      it reopens the row; simulated 401 expands the row with the message.

## 4. List readability

- [x] 4.1 Relative timestamps (D6): `Intl.RelativeTimeFormat` for created
      times in list and detail; full locale datetime in the element's
      `title`. Acceptance: fresh mock timestamps render as "just now"/"N
      days ago"; tooltip holds the absolute datetime.
- [x] 4.2 Identifier line and empty state (D10): hide the identifier when it
      equals the slug; empty list state renders an Upload CTA linking to
      `#/upload`. Acceptance: mock rows with equal and distinct identifiers;
      empty mocked list shows the CTA and navigates.

## 5. Upload drop zone

- [x] 5.1 Styled drop zone (D7): label-wrapped visually hidden input for
      click-to-browse, `dragover`/`drop` handlers, client-side extension
      filter (.html/.htm/.zip) with error before any request, chosen file
      name and size displayed; submit path unchanged. Acceptance: browser
      check — drop selects and uploads; unsupported extension errors
      client-side with no request sent; keyboard browse still works.

## 6. Overflow guard

- [x] 6.1 Wrap list and manifest tables in an `overflow-x: auto` container
      (D9). Acceptance: at a 390px viewport the tables scroll horizontally
      inside the container and Park/Delete stay reachable.

## 7. Verification

- [x] 7.1 Go gates green after the embed change: `gofmt -l .` empty, `go vet
      ./...` and `go vet -tags=integration ./...` clean, `go test ./...`
      green, and `go test -tags=integration ./...` green (no Go behavior
      changed; embed + plane-mount tests must still pass).
- [x] 7.2 Browser QA of every scenario in the `admin-ui` delta spec
      (prefer the openspec-verify-change skill): all three views, 401 flow,
      empty state, narrow viewport, drag-and-drop.
