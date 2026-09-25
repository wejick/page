// Package serve implements the serving layer: URL→key arithmetic (D13),
// cache headers split per plane (assets immutable, entry HTML revalidatable),
// the TTL-revalidated in-memory HTML cache (D14), and the mode-scoped router
// (serve → page/asset routes, admin → upload UI + API, all → everything).
// The serve path never queries the database.
package serve

import (
	"context"
	"embed"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"page/internal/config"
	"page/internal/lifecycle"
	"page/internal/storage"
	"page/internal/upload"
)

//go:embed static/index.html
var staticFS embed.FS

// Options carries the handler dependencies.
type Options struct {
	Store         storage.Storage
	Mode          config.Mode                 // planes to mount: serve, admin, or all; zero value behaves as all
	CacheMaxBytes int64                       // htmlCache budget; 0 → default 256 MiB
	CacheTTL      time.Duration               // entry-HTML revalidation interval; 0 → default 60s
	Upload        *upload.Handler             // upload API (admin/all planes)
	Lifecycle     *lifecycle.API              // park/unpark endpoints (admin/all planes); nil omits them
	Ping          func(context.Context) error // health probe; storage Stat in serve mode, Postgres ping in admin/all
}

// Handler serves pages and assets.
type Handler struct {
	store storage.Storage
	cache *htmlCache
	api   *upload.Handler
	ping  func(context.Context) error
}

// New builds the service router, scoped to the planes Options.Mode selects:
// serve mounts only the page/asset routes and health, admin only the upload
// UI and admin API, all (including the zero value) everything.
func New(o Options) http.Handler {
	h := &Handler{store: o.Store, api: o.Upload, ping: o.Ping}
	h.cache = newHTMLCache(o.CacheMaxBytes, o.CacheTTL)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)

	// Upload API (auth inside the handler, D9). Serve mode never builds
	// these deps; a nil one simply mounts nothing instead of panicking.
	api := func() {
		if o.Upload != nil {
			mux.Handle("POST /api/pages", o.Upload.Create())
			mux.Handle("GET /api/pages/{slug}", o.Upload.Get())
		}
		// Park/unpark toggle (auth inside the handler).
		if o.Lifecycle != nil {
			mux.Handle("POST /api/pages/{slug}/park", o.Lifecycle.Park())
			mux.Handle("POST /api/pages/{slug}/unpark", o.Lifecycle.Unpark())
		}
	}
	pages := func() {
		// Pages: slashless redirects so relative refs resolve (D13).
		mux.HandleFunc("GET /p/{slug}", h.redirectSlash)
		mux.HandleFunc("GET /p/{slug}/{$}", h.pageIndex)
		mux.HandleFunc("GET /p/{slug}/{rest...}", h.pageAsset)
		// Dual-mount: /a/{slug}/* maps to the same bucket keys (D5).
		mux.HandleFunc("GET /a/{slug}/{rest...}", h.asset)
	}

	// ModeAdmin mounts only the admin plane; ModeServe only the serving
	// plane. The zero Mode value equals neither named mode, so it passes
	// both guards and mounts everything — the documented zero-value-as-all
	// behavior.
	if o.Mode != config.ModeServe {
		mux.HandleFunc("GET /{$}", h.ui)
		api()
	}
	if o.Mode != config.ModeAdmin {
		pages()
	}

	return mux
}

// Cache-control policies: assets are immutable bytes cached for a year;
// entry HTML is small and revalidates quickly so lifecycle changes (parking)
// propagate through caches within a bounded window (page-lifecycle spec).
const (
	cacheControlAsset = "public, max-age=31536000, immutable"
	cacheControlEntry = "public, max-age=60, must-revalidate"
)

func (h *Handler) ui(w http.ResponseWriter, r *http.Request) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// healthProbeKey is the fixed, unlikely object key StorageProbe stats to prove
// the storage round-trip (deployment-modes D4). It lives outside every page's
// {slug}/ prefix, so it is normally absent — ErrNotFound, which counts as
// healthy.
const healthProbeKey = "_health/probe"

// StorageProbe returns a health probe for serve-mode instances: one Stat on
// the probe key. Any storage response — including ErrNotFound for the absent
// key — proves the round-trip and reports healthy; any other error (transport
// failure) is returned so healthz reports 503. Serve health never touches the
// database (deployment-modes D4).
func StorageProbe(store storage.Storage) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := store.Stat(ctx, healthProbeKey)
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	if h.ping != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := h.ping(ctx); err != nil {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) redirectSlash(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !validSlugSegment(slug) {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/p/"+slug+"/", http.StatusMovedPermanently)
}

// pageIndex serves the entry HTML, through the in-memory cache. Entries are
// revalidated via Stat after the cache TTL, so a parked page stops being
// served within that window even if its bytes were cached.
func (h *Handler) pageIndex(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !validSlugSegment(slug) {
		http.NotFound(w, r)
		return
	}
	key := slug + "/index.html"
	if e, present, stale := h.cache.lookup(slug); present {
		if !stale {
			h.writeBytes(w, r, e.data, e.contentType, e.etag, cacheControlEntry)
			return
		}
		meta, err := h.store.Stat(r.Context(), key)
		if errors.Is(err, storage.ErrNotFound) {
			h.cache.evict(slug)
			h.serveError(w, r, err)
			return
		}
		if err != nil {
			h.serveError(w, r, err)
			return
		}
		if meta.ETag == e.etag {
			// Still there and unchanged: refresh and serve from memory.
			h.cache.touch(slug)
			h.writeBytes(w, r, e.data, e.contentType, e.etag, cacheControlEntry)
			return
		}
		// ETag changed (not expected for immutable content): fall through
		// to a full refetch below rather than serve divergent bytes.
	}
	obj, err := h.store.Get(r.Context(), key)
	if err != nil {
		h.serveError(w, r, err)
		return
	}
	data, err := io.ReadAll(obj.Reader)
	_ = obj.Reader.Close()
	if err != nil {
		h.serveError(w, r, err)
		return
	}
	h.cache.put(slug, data, obj.ContentType, obj.ETag)
	h.writeBytes(w, r, data, obj.ContentType, obj.ETag, cacheControlEntry)
}

// pageAsset serves /p/{slug}/{rest} as key {slug}/{rest}: the dual-mount
// alias for runtime-constructed relative refs (D5).
func (h *Handler) pageAsset(w http.ResponseWriter, r *http.Request) {
	h.serveAsset(w, r, r.PathValue("slug"), r.PathValue("rest"))
}

// asset serves /a/{slug}/{rest} as key {slug}/{rest}: what rewritten refs use.
func (h *Handler) asset(w http.ResponseWriter, r *http.Request) {
	h.serveAsset(w, r, r.PathValue("slug"), r.PathValue("rest"))
}

func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request, slug, rest string) {
	if !validSlugSegment(slug) || !validRest(rest) {
		http.NotFound(w, r)
		return
	}
	obj, err := h.store.Get(r.Context(), slug+"/"+rest)
	if err != nil {
		h.serveError(w, r, err)
		return
	}
	data, err := io.ReadAll(obj.Reader)
	_ = obj.Reader.Close()
	if err != nil {
		h.serveError(w, r, err)
		return
	}
	h.writeBytes(w, r, data, obj.ContentType, obj.ETag, cacheControlAsset)
}

// writeBytes sets the content headers and honors If-None-Match. The
// cache-control policy differs per plane: assets immutable, entry HTML
// revalidatable (see the constants above).
func (h *Handler) writeBytes(w http.ResponseWriter, r *http.Request, data []byte, contentType, etag, cacheControl string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", `"`+etag+`"`)
	if ifNoneMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *Handler) serveError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	slog.Error("serve", "path", r.URL.Path, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// validSlugSegment ensures the slug is one clean path segment.
func validSlugSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}

// validRest rejects traversal segments and empty rests.
func validRest(rest string) bool {
	if rest == "" {
		return false
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ifNoneMatch implements a lenient If-None-Match comparison.
func ifNoneMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		part = strings.Trim(part, `"`)
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}
