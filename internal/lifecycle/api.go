package lifecycle

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// API exposes the toggle endpoints over HTTP. Mounted by the serve package:
//
//	POST /api/pages/{slug}/park
//	POST /api/pages/{slug}/unpark
type API struct {
	svc   *Service
	token string
}

// NewAPI builds the toggle API around a Service.
func NewAPI(svc *Service, token string) *API {
	return &API{svc: svc, token: token}
}

// Park handles POST /api/pages/{slug}/park.
func (a *API) Park() http.Handler { return http.HandlerFunc(a.park) }

// Unpark handles POST /api/pages/{slug}/unpark.
func (a *API) Unpark() http.Handler { return http.HandlerFunc(a.unpark) }

// Delete handles DELETE /api/pages/{slug}: permanent removal, not a toggle.
func (a *API) Delete() http.Handler { return http.HandlerFunc(a.remove) }

func (a *API) park(w http.ResponseWriter, r *http.Request) {
	a.toggle(w, r, a.svc.Park)
}

func (a *API) unpark(w http.ResponseWriter, r *http.Request) {
	a.toggle(w, r, a.svc.Unpark)
}

// remove is Delete's handler. Unlike the toggles it returns no terminal
// status (the row is gone), and its 409 message names any in-flight
// transition rather than an "opposite" one — delete blocks on both
// directions.
func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	if !a.auth(w, r) {
		return
	}
	switch err := a.svc.Delete(r.Context(), r.PathValue("slug")); {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case isNotFound(err):
		http.NotFound(w, r)
	case isBusy(err):
		http.Error(w, "lifecycle transition in progress, retry shortly", http.StatusConflict)
	default:
		slog.Error("lifecycle", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (a *API) toggle(w http.ResponseWriter, r *http.Request, run func(ctx context.Context, slug string) (string, error)) {
	if !a.auth(w, r) {
		return
	}
	status, err := run(r.Context(), r.PathValue("slug"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	case isNotFound(err):
		http.NotFound(w, r)
	case isBusy(err):
		http.Error(w, "opposite toggle in progress, retry shortly", http.StatusConflict)
	default:
		slog.Error("lifecycle", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// auth enforces the bearer token with a constant-time compare (D9, mirroring
// the upload handler).
func (a *API) auth(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "Bearer "
	got := strings.TrimPrefix(r.Header.Get("Authorization"), prefix)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
func isBusy(err error) bool     { return errors.Is(err, ErrBusy) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
