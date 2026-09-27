// Package upload implements the authenticated upload API: multipart intake
// (file or source URL), validation and zip-safety caps, slug assignment,
// ingest, storage, and page persistence. Handlers are mounted by the serve
// package.
package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"page/internal/auth"
	"page/internal/config"
	"page/internal/db"
	"page/internal/fetch"
	"page/internal/ingest"
	"page/internal/slug"
	"page/internal/storage"
)

// Handler serves the upload API.
type Handler struct {
	pool  *pgxpool.Pool
	store storage.Storage
	caps  config.Caps
	keep  ingest.KeepRules
	authn *auth.Checker
	guard fetch.GuardFunc
}

// Options carries handler dependencies.
type Options struct {
	Pool  *pgxpool.Pool
	Store storage.Storage
	Caps  config.Caps
	Keep  ingest.KeepRules
	Auth  *auth.Checker   // admin-plane authenticator (auth-modes D7)
	Guard fetch.GuardFunc // SSRF guard for outbound fetches; nil → fetch.Standard
}

// New builds the API handler.
func New(o Options) *Handler {
	guard := o.Guard
	if guard == nil {
		guard = fetch.Standard
	}
	return &Handler{pool: o.Pool, store: o.Store, caps: o.Caps, keep: o.Keep, authn: o.Auth, guard: guard}
}

// Create handles POST /api/pages.
func (h *Handler) Create() http.Handler { return http.HandlerFunc(h.create) }

// Get handles GET /api/pages/{slug}.
func (h *Handler) Get() http.Handler { return http.HandlerFunc(h.get) }

// List handles GET /api/pages.
func (h *Handler) List() http.Handler { return http.HandlerFunc(h.list) }

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}

	// Cap the raw body (multipart framing overhead accounted for).
	r.Body = http.MaxBytesReader(w, r.Body, h.caps.MaxRawBytes+(1<<20))
	srcURL := strings.TrimSpace(r.FormValue("url"))
	file, _, fileErr := r.FormFile("file")
	hasFile := fileErr == nil
	switch {
	case srcURL != "" && hasFile:
		file.Close()
		http.Error(w, "provide either 'url' or 'file', not both", http.StatusUnprocessableEntity)
		return
	case srcURL == "" && !hasFile:
		if strings.Contains(fmt.Sprint(fileErr), "request body too large") {
			http.Error(w, "upload exceeds raw size cap", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "multipart form: missing 'file' field", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	lim := ingest.Limits{
		MaxFiles:        h.caps.MaxFiles,
		MaxDecompressed: h.caps.MaxDecompressedBytes,
		MaxAssetBytes:   h.caps.MaxAssetBytes,
	}

	var files map[string][]byte
	var baseURL *url.URL
	if srcURL != "" {
		// URL import (import-by-url D1/D2): fetch the entry document, then
		// feed it into the machinery with its final URL as the base.
		entry, err := fetch.NewEntryFetcher(h.caps.MaxRawBytes, h.caps.FetchTimeout, h.guard).
			Fetch(ctx, srcURL)
		if err != nil {
			status, msg := fetchFailure(err)
			http.Error(w, msg, status)
			return
		}
		files = map[string][]byte{"index.html": entry.Data}
		baseURL = entry.FinalURL
	} else {
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "upload exceeds raw size cap", http.StatusRequestEntityTooLarge)
			return
		}
		if int64(len(data)) > h.caps.MaxRawBytes {
			http.Error(w, "upload exceeds raw size cap", http.StatusRequestEntityTooLarge)
			return
		}
		identifier := r.FormValue("identifier")
		if identifier == "" {
			identifier = "page" // default identifier (spec: identifier omitted)
		}
		// Route by content: zip pack or single HTML.
		if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
			files, err = ingest.OpenPack(data, lim)
			if err != nil {
				if isSizeErr(err) {
					http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
		} else if fetch.LooksLikeHTML(data) {
			files = map[string][]byte{"index.html": data}
		} else {
			http.Error(w, "upload must be a single HTML file or a zip pack", http.StatusUnsupportedMediaType)
			return
		}
	}

	identifier := r.FormValue("identifier")
	if identifier == "" {
		identifier = "page" // default identifier (spec: identifier omitted)
	}

	entry, err := ingest.DetectEntry(files)
	if err != nil {
		http.Error(w, "pack contains no HTML entry", http.StatusUnprocessableEntity)
		return
	}

	// Slug assignment + ingest + persistence, with retry on the unique-index
	// backstop. Ingest is slug-dependent (refs rewrite to /a/{slug}/...), so
	// it runs inside the retry loop; the fetch budget is per upload (D10).
	for attempt := 0; attempt < 3; attempt++ {
		slugStr, code, err := slug.New(ctx, h.pool, identifier)
		if err != nil {
			if errors.Is(err, slug.ErrInvalidIdentifier) || errors.Is(err, slug.ErrReserved) {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			h.fail(w, r, "slug allocate", err)
			return
		}

		fetcher := ingest.NewFetcher(h.caps.MaxAssetBytes, h.caps.FetchTimeout,
			h.caps.FetchBudget, h.caps.FetchConcurrency, h.guard)
		res, err := (&ingest.Pipeline{Keep: h.keep, Fetch: fetcher}).
			Process(ctx, files, entry, slugStr, baseURL)
		if err != nil {
			_ = h.store.DeletePrefix(ctx, slugStr+"/")
			h.fail(w, r, "ingest", err)
			return
		}

		// Strict completeness gate (import-by-url D5): an import is
		// all-or-nothing — any asset we could not fetch would publish a
		// broken copy. Uploads stay best-effort: their user already has the
		// bytes in hand.
		if baseURL != nil {
			unresolved := unresolvedAssets(res)
			if len(unresolved) > 0 {
				_ = h.store.DeletePrefix(ctx, slugStr+"/")
				slog.Info("upload", "what", "import rejected", "slug", slugStr,
					"unresolved", len(unresolved), "source", baseURL)
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": "import_incomplete",
					"message": fmt.Sprintf(
						"%d asset%s on the source page could not be fetched. The import was rejected rather than publish a broken copy. Save the page in your browser (Save As or SingleFile) and upload the file instead.",
						len(unresolved), plural(len(unresolved))),
					"unresolved": unresolved,
				})
				return
			}
		}

		if err := h.putObjects(ctx, slugStr, res); err != nil {
			_ = h.store.DeletePrefix(ctx, slugStr+"/")
			h.fail(w, r, "store objects", err)
			return
		}

		assetRows := make([]db.AssetRow, len(res.Manifest))
		var total int64 = int64(len(res.Entry))
		for i, m := range res.Manifest {
			assetRows[i] = db.AssetRow{
				Path: m.Path, SourceURL: m.SourceURL,
				ContentType: m.ContentType, Bytes: m.Bytes, Status: m.Status,
			}
			total += m.Bytes
		}
		err = db.CreatePage(ctx, h.pool, db.PageRecord{
			Slug: slugStr, Identifier: slugIdentifier(slugStr), Code: code,
		}, assetRows, total)
		if err != nil {
			_ = h.store.DeletePrefix(ctx, slugStr+"/")
			if slug.IsUniqueViolation(err) {
				continue // backstop hit: re-allocate and try again
			}
			h.fail(w, r, "persist page", err)
			return
		}

		counts := map[string]int{}
		for _, m := range res.Manifest {
			counts[m.Status]++
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"slug":       slugStr,
			"identifier": slugIdentifier(slugStr),
			"code":       code,
			"url":        "/p/" + slugStr + "/",
			"assets": map[string]int{
				"local":         counts[ingest.StatusLocal],
				"baked":         counts[ingest.StatusBaked],
				"kept_cdn":      counts[ingest.StatusKeptCDN],
				"kept_external": counts[ingest.StatusKeptExternal],
			},
		})
		return
	}
	http.Error(w, "could not allocate a unique slug", http.StatusInternalServerError)
}

// Get handles GET /api/pages/{slug}.
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	meta, assets, err := db.GetPage(r.Context(), h.pool, r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.fail(w, r, "get page", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"slug": meta.Slug, "identifier": meta.Identifier, "code": meta.Code,
		"url": "/p/" + meta.Slug + "/", "asset_count": meta.AssetCount,
		"total_bytes": meta.TotalBytes, "created_at": meta.CreatedAt,
		"status": meta.Status,
		"assets": assets,
	})
}

// list serves the paginated page list for the management UI
// (add-admin-management-ui D4). Garbage limit/offset values fall back to the
// defaults; db.ListPages does the clamping.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	pages, total, err := db.ListPages(r.Context(), h.pool, q.Get("status"), limit, offset)
	if err != nil {
		h.fail(w, r, "list pages", err)
		return
	}
	items := make([]map[string]any, 0, len(pages))
	for _, p := range pages {
		items = append(items, map[string]any{
			"slug": p.Slug, "identifier": p.Identifier, "code": p.Code,
			"status": p.Status, "asset_count": p.AssetCount,
			"total_bytes": p.TotalBytes, "created_at": p.CreatedAt,
			"url": "/p/" + p.Slug + "/",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "pages": items})
}

// putObjects stores the normalized entry and every asset under {slug}/.
func (h *Handler) putObjects(ctx context.Context, slugStr string, res *ingest.Result) error {
	if err := h.store.Put(ctx, slugStr+"/index.html", "text/html; charset=utf-8",
		bytes.NewReader(res.Entry)); err != nil {
		return err
	}
	paths := make([]string, 0, len(res.Files))
	for p := range res.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := res.Files[p]
		if err := h.store.Put(ctx, slugStr+"/"+p, f.ContentType, bytes.NewReader(f.Data)); err != nil {
			return err
		}
	}
	return nil
}

// auth delegates to the shared admin-plane checker (auth-modes D7); every
// mode's accept rule lives there.
func (h *Handler) auth(w http.ResponseWriter, r *http.Request) bool {
	return h.authn.Allow(w, r, auth.KindAPI)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.Error("upload", "what", what, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func isSizeErr(err error) bool {
	return strings.Contains(err.Error(), "exceeds") || strings.Contains(err.Error(), "cap")
}

// fetchFailure maps typed entry-fetch failures to a status code and message
// in one place (import-by-url D6): caller mistakes are 4xx, source-side
// failures are 502.
func fetchFailure(err error) (int, string) {
	switch {
	case errors.Is(err, fetch.ErrInvalidURL):
		return http.StatusUnprocessableEntity, "invalid source url"
	case errors.Is(err, fetch.ErrBlocked):
		return http.StatusUnprocessableEntity, "source url is not fetchable (blocked address)"
	case errors.Is(err, fetch.ErrNotHTML):
		return http.StatusUnsupportedMediaType, "source did not return an HTML document"
	case errors.Is(err, fetch.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "source response exceeds entry size cap"
	default:
		return http.StatusBadGateway, "could not fetch source url"
	}
}

// unresolvedAssets lists the manifest's kept-external rows: the import
// gate's completeness report (import-by-url D5).
func unresolvedAssets(res *ingest.Result) []map[string]string {
	var out []map[string]string
	for _, m := range res.Manifest {
		if m.Status != ingest.StatusKeptExternal {
			continue
		}
		reason := m.Reason
		if reason == "" {
			reason = "unfetchable"
		}
		out = append(out, map[string]string{"url": m.SourceURL, "reason": reason})
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// slugIdentifier recovers the identifier part; the slug is "{identifier}-{code}"
// and the code is the last dash-separated segment (allocation is the only
// writer, so this is exact at creation time).
func slugIdentifier(slugStr string) string {
	if i := strings.LastIndex(slugStr, "-"); i > 0 {
		return slugStr[:i]
	}
	return slugStr
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
