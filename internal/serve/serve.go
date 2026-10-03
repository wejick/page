// Package serve implements the serving layer: URL→key arithmetic (D13),
// cache headers split per plane (assets immutable, entry HTML revalidatable),
// the TTL-revalidated in-memory HTML cache (D14), and the mode-scoped router
// (serve → page/asset routes, admin → upload UI + API, all → everything).
// The serve path never queries the database.
package serve

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"page/internal/auth"
	"page/internal/config"
	"page/internal/httpx"
	"page/internal/lifecycle"
	"page/internal/storage"
	"page/internal/upload"
)

//go:embed static
var staticFS embed.FS

// Options carries the handler dependencies.
type Options struct {
	Store         storage.Storage
	Mode          config.Mode                 // planes to mount: serve, admin, or all; zero value behaves as all
	CacheMaxBytes int64                       // htmlCache budget; 0 → default 256 MiB
	CacheTTL      time.Duration               // entry-HTML revalidation interval; 0 → default 60s
	Upload        *upload.Handler             // upload API (admin/all planes)
	Lifecycle     *lifecycle.API              // park/unpark endpoints (admin/all planes); nil omits them
	Auth          *auth.Checker               // admin-plane auth (auth-modes D7); nil or flowless fails closed
	Ping          func(context.Context) error // health probe; storage Stat in serve mode, database ping in admin/all

	// Observability (observability D1, D5): nil → slog default logger and a
	// noop meter, so tests and serve mode boot with identical behavior.
	Log   *slog.Logger
	Meter metric.Meter
}

// Handler serves pages and assets.
type Handler struct {
	store storage.Storage
	cache *htmlCache
	api   *upload.Handler
	authn *auth.Checker
	ping  func(context.Context) error

	log       *slog.Logger
	instr     instrumentation
	startedAt time.Time
	version   string
}

// New builds the service router, scoped to the planes Options.Mode selects:
// serve mounts only the page/asset routes and health, admin only the upload
// UI and admin API, all (including the zero value) everything. Every route
// is registered through withRoute, and the mux is wrapped in the
// request-id/recovery/access-log chain (observability D3, D4).
func New(o Options) http.Handler {
	h := &Handler{store: o.Store, api: o.Upload, authn: o.Auth, ping: o.Ping,
		startedAt: time.Now(), version: ModuleVersion()}
	h.log = o.Log
	if h.log == nil {
		h.log = slog.Default()
	}
	meter := o.Meter
	if meter == nil {
		meter = noop.NewMeterProvider().Meter("page")
	}
	h.instr = newInstrumentation(meter)
	h.cache = newHTMLCache(o.CacheMaxBytes, o.CacheTTL, meter)

	mux := http.NewServeMux()
	route := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, h.withRoute(pattern, handler))
	}
	handleFunc := func(pattern string, handler http.HandlerFunc) {
		route(pattern, handler)
	}

	handleFunc("GET /healthz", h.healthz)

	// Upload API (auth inside the handler via the shared checker, auth-modes
	// D7). Serve mode never builds these deps; a nil one simply mounts
	// nothing instead of panicking.
	api := func() {
		if o.Upload != nil {
			route("POST /api/pages", o.Upload.Create())
			route("GET /api/pages/{slug}", o.Upload.Get())
			route("GET /api/pages", o.Upload.List())
		}
		// Park/unpark toggle and hard delete (same checker).
		if o.Lifecycle != nil {
			route("POST /api/pages/{slug}/park", o.Lifecycle.Park())
			route("POST /api/pages/{slug}/unpark", o.Lifecycle.Unpark())
			route("DELETE /api/pages/{slug}", o.Lifecycle.Delete())
		}
	}
	pages := func() {
		// Pages: slashless redirects so relative refs resolve (D13).
		handleFunc("GET /p/{slug}", h.redirectSlash)
		handleFunc("GET /p/{slug}/{$}", h.pageIndex)
		handleFunc("GET /p/{slug}/{rest...}", h.pageAsset)
		// Dual-mount: /a/{slug}/* maps to the same bucket keys (D5).
		handleFunc("GET /a/{slug}/{rest...}", h.asset)
	}

	// ModeAdmin mounts only the admin plane; ModeServe only the serving
	// plane. The zero Mode value equals neither named mode, so it passes
	// both guards and mounts everything — the documented zero-value-as-all
	// behavior.
	if o.Mode != config.ModeServe {
		handleFunc("GET /{$}", h.ui)
		handleFunc("GET /ui/{file}", h.uiAsset)
		api()
		// The browser half of the OIDC flow mounts only when the flow is
		// actually wired (auth-modes D5): a flowless oidc checker fails
		// closed instead of mounting nil handlers.
		if o.Auth.HasFlow() {
			route("GET /login", o.Auth.Login())
			route("GET /auth/callback", o.Auth.Callback())
			route("POST /logout", o.Auth.Logout())
		}
	}
	if o.Mode != config.ModeAdmin {
		pages()
	}

	return h.instrument(mux)
}

// ModuleVersion reports the serving binary's module version for /healthz
// and the boot trail (observability: build identification). Development
// builds report "dev".
func ModuleVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}

// Cache-control policies: assets are immutable bytes cached for a year;
// entry HTML is small and revalidates quickly so lifecycle changes (parking)
// propagate through caches within a bounded window (page-lifecycle spec).
const (
	cacheControlAsset = "public, max-age=31536000, immutable"
	cacheControlEntry = "public, max-age=60, must-revalidate"
	// Shell application assets revalidate unconditionally: they are tiny
	// (admin plane only) and their bytes change with every deploy, so
	// caching them longer buys nothing but stale-UI bugs (harden-admin-ui D2).
	cacheControlUIAsset = "no-cache"
)

// uiContentSecurityPolicy locks the UI shell down to same-origin scripts,
// styles and connections (harden-admin-ui D3): no inline script or style,
// nothing from any other origin, nobody frames the admin UI. Enforced, not
// report-only; the shell and its /ui/* assets are built to satisfy it.
const uiContentSecurityPolicy = "default-src 'none'; script-src 'self'; " +
	"style-src 'self'; connect-src 'self'; img-src 'self' data:; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// setUISecurityHeaders stamps the shell's security headers. writeBytes
// already sets nosniff for /ui/* responses; setting it here too keeps both
// shell surfaces stamped by one call.
func setUISecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", uiContentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// validTheme reports whether s is a theme choice the shell understands.
// Anything else (including "system", the default) injects no attribute so
// the palette follows prefers-color-scheme (harden-admin-ui D4).
func validTheme(s string) bool { return s == "light" || s == "dark" }

// ui serves the management shell. In oidc mode an unauthenticated browser is
// redirected to /login (auth-modes D7); the shell always learns its auth
// mode from the injected data-auth-mode attribute (auth-modes D8) and its
// theme from the injected data-theme attribute (harden-admin-ui D4) — set
// before first paint from the sp-theme cookie, so the correct palette
// renders with no flash and no inline script (which the CSP bans).
func (h *Handler) ui(w http.ResponseWriter, r *http.Request) {
	// Headers go on every / response, including the oidc redirect an
	// unauthenticated browser gets — the whole surface is the shell's.
	setUISecurityHeaders(w)
	if h.authn != nil && !h.authn.Allow(w, r, auth.KindBrowser) {
		return
	}
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	mode := "token"
	if h.authn != nil {
		mode = string(h.authn.Mode())
	}
	page = []byte(strings.Replace(string(page),
		`data-auth-mode="token"`, `data-auth-mode="`+mode+`"`, 1))
	if theme, err := r.Cookie("sp-theme"); err == nil && validTheme(theme.Value) {
		page = []byte(strings.Replace(string(page),
			`<html lang="en">`, `<html lang="en" data-theme="`+theme.Value+`">`, 1))
	}
	setUISecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// uiContentTypes pins the asset content types; nosniff makes guesses
// dangerous, so nothing here sniffs.
var uiContentTypes = map[string]string{
	"css": "text/css; charset=utf-8",
	"js":  "text/javascript; charset=utf-8",
}

// isVendoredAlpine reports whether the asset is the versioned vendored
// Alpine build — its name pins the exact upstream release, so its bytes are
// immutable and it caches forever (harden-admin-ui D2).
func isVendoredAlpine(name string) bool {
	return strings.HasPrefix(name, "alpine.csp-") && strings.HasSuffix(name, ".min.js")
}

// validAssetName admits exactly the shell's asset files — nothing else in
// the embed is reachable over HTTP.
func validAssetName(name string) bool {
	switch name {
	case "app.css", "app.js":
		return true
	default:
		return isVendoredAlpine(name)
	}
}

// uiAsset serves the shell's embedded static assets under /ui/{file}. The
// path value is one clean segment (no slashes), so nothing outside static/
// is reachable. Application assets revalidate, since their bytes change
// with every deploy (harden-admin-ui D2).
func (h *Handler) uiAsset(w http.ResponseWriter, r *http.Request) {
	setUISecurityHeaders(w)
	name := r.PathValue("file")
	if !validAssetName(name) {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct, ok := uiContentTypes[strings.TrimPrefix(filepath.Ext(name), ".")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	cacheControl := cacheControlUIAsset
	if isVendoredAlpine(name) {
		cacheControl = cacheControlAsset
	}
	h.writeBytes(w, r, data, ct, uiETag(name, data), cacheControl)
}

// uiETags caches one sha256 per embedded asset: immutable bytes, immutable
// ETag, computed once per process.
var uiETags sync.Map // name → etag

func uiETag(name string, data []byte) string {
	if etag, ok := uiETags.Load(name); ok {
		return etag.(string)
	}
	sum := sha256.Sum256(data)
	etag := hex.EncodeToString(sum[:16])
	uiETags.Store(name, etag)
	return etag
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

// healthz reports the probe outcome plus build identity (observability:
// health responses identify the build): status codes are unchanged — 200
// healthy, 503 unhealthy — and the body is JSON with version and uptime.
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	status := map[bool]string{true: "ok", false: "unhealthy"}
	code := http.StatusOK
	if h.ping != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := h.ping(ctx); err != nil {
			code = http.StatusServiceUnavailable
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  status[code == http.StatusOK],
		"version": h.version,
		"uptime":  time.Since(h.startedAt).Round(time.Second).String(),
	})
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
	if e, present, stale := h.cache.lookup(r.Context(), slug); present {
		if !stale {
			h.writeBytes(w, r, e.data, e.contentType, e.etag, cacheControlEntry)
			return
		}
		meta, err := h.store.Stat(r.Context(), key)
		if errors.Is(err, storage.ErrNotFound) {
			h.cache.evict(r.Context(), slug)
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
	h.cache.put(r.Context(), slug, data, obj.ContentType, obj.ETag)
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
	// The request-scoped logger carries request_id and route (observability
	// D3); the fallback covers handlers invoked without the middleware.
	httpx.Log(r.Context(), h.log).Error("serve", "path", r.URL.Path, "err", err)
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
