// Package upload implements the authenticated upload API: multipart intake,
// validation and zip-safety caps, slug assignment, ingest, storage, and
// page persistence. Handlers are mounted by the serve package.
package upload

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"page/internal/config"
	"page/internal/db"
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
	token string
}

// Options carries handler dependencies.
type Options struct {
	Pool  *pgxpool.Pool
	Store storage.Storage
	Caps  config.Caps
	Keep  ingest.KeepRules
	Token string
}

// New builds the API handler.
func New(o Options) *Handler {
	return &Handler{pool: o.Pool, store: o.Store, caps: o.Caps, keep: o.Keep, token: o.Token}
}

// Create handles POST /api/pages.
func (h *Handler) Create() http.Handler { return http.HandlerFunc(h.create) }

// Get handles GET /api/pages/{slug}.
func (h *Handler) Get() http.Handler { return http.HandlerFunc(h.get) }

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}

	// Cap the raw body (multipart framing overhead accounted for).
	r.Body = http.MaxBytesReader(w, r.Body, h.caps.MaxRawBytes+(1<<20))
	file, _, err := r.FormFile("file")
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			http.Error(w, "upload exceeds raw size cap", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "multipart form: missing 'file' field", http.StatusBadRequest)
		return
	}
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
	lim := ingest.Limits{
		MaxFiles:        h.caps.MaxFiles,
		MaxDecompressed: h.caps.MaxDecompressedBytes,
		MaxAssetBytes:   h.caps.MaxAssetBytes,
	}
	var files map[string][]byte
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
	} else if looksLikeHTML(data) {
		files = map[string][]byte{"index.html": data}
	} else {
		http.Error(w, "upload must be a single HTML file or a zip pack", http.StatusUnsupportedMediaType)
		return
	}

	entry, err := ingest.DetectEntry(files)
	if err != nil {
		http.Error(w, "pack contains no HTML entry", http.StatusUnprocessableEntity)
		return
	}

	// Slug assignment + ingest + persistence, with retry on the unique-index
	// backstop. Ingest is slug-dependent (refs rewrite to /a/{slug}/...), so
	// it runs inside the retry loop; the fetch budget is per upload (D10).
	ctx := r.Context()
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
			h.caps.FetchBudget, h.caps.FetchConcurrency)
		res, err := (&ingest.Pipeline{Keep: h.keep, Fetch: fetcher}).
			Process(ctx, files, entry, slugStr)
		if err != nil {
			_ = h.store.DeletePrefix(ctx, slugStr+"/")
			h.fail(w, r, "ingest", err)
			return
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

// auth enforces the bearer token with a constant-time compare (D9).
func (h *Handler) auth(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "Bearer "
	got := strings.TrimPrefix(r.Header.Get("Authorization"), prefix)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.Error("upload", "what", what, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func isSizeErr(err error) bool {
	return strings.Contains(err.Error(), "exceeds") || strings.Contains(err.Error(), "cap")
}

// htmlMarkers identify an HTML document even when served as text/plain.
var htmlMarkers = []string{
	"<!doctype html", "<html", "<head", "<body", "<div", "<script", "<img ",
}

func looksLikeHTML(data []byte) bool {
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	if strings.HasPrefix(http.DetectContentType(sample), "text/html") {
		return true
	}
	low := strings.ToLower(string(sample))
	for _, m := range htmlMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
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
