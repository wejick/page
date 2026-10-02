package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"page/internal/auth"
	"page/internal/httpx"
)

// API exposes the toggle endpoints over HTTP. Mounted by the serve package:
//
//	POST /api/pages/{slug}/park
//	POST /api/pages/{slug}/unpark
type API struct {
	svc   *Service
	authn *auth.Checker
	log   *slog.Logger
	ops   metric.Int64Counter // lifecycle.ops: op, outcome
}

// NewAPI builds the toggle API around a Service. log and meter may be nil
// → slog default and noop metrics (observability D1, D5).
func NewAPI(svc *Service, authn *auth.Checker, log *slog.Logger, m metric.Meter) *API {
	if log == nil {
		log = slog.Default()
	}
	if m == nil {
		m = noop.NewMeterProvider().Meter("page")
	}
	ops, err := m.Int64Counter("lifecycle.ops",
		metric.WithDescription("Lifecycle operations by op and outcome"))
	if err != nil {
		ops, _ = noop.NewMeterProvider().Meter("page").Int64Counter("lifecycle.ops")
	}
	return &API{svc: svc, authn: authn, log: log, ops: ops}
}

// Park handles POST /api/pages/{slug}/park.
func (a *API) Park() http.Handler { return http.HandlerFunc(a.park) }

// Unpark handles POST /api/pages/{slug}/unpark.
func (a *API) Unpark() http.Handler { return http.HandlerFunc(a.unpark) }

// Delete handles DELETE /api/pages/{slug}: permanent removal, not a toggle.
func (a *API) Delete() http.Handler { return http.HandlerFunc(a.remove) }

func (a *API) park(w http.ResponseWriter, r *http.Request) {
	a.toggle(w, r, "park", a.svc.Park)
}

func (a *API) unpark(w http.ResponseWriter, r *http.Request) {
	a.toggle(w, r, "unpark", a.svc.Unpark)
}

// outcome maps a response status onto the fixed outcome label set.
func outcome(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "ok"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "busy"
	case status >= 400 && status < 500:
		return "rejected"
	default:
		return "error"
	}
}

// record adds one op observation with the captured status.
func (a *API) record(r *http.Request, op string, status int) {
	a.ops.Add(r.Context(), 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("outcome", outcome(status))))
}

// remove is Delete's handler. Unlike the toggles it returns no terminal
// status (the row is gone), and its 409 message names any in-flight
// transition rather than an "opposite" one — delete blocks on both
// directions.
func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !a.auth(w, r) {
		a.record(r, "delete", http.StatusUnauthorized)
		return
	}
	switch err := a.svc.Delete(r.Context(), r.PathValue("slug")); {
	case err == nil:
		a.record(r, "delete", http.StatusOK)
		a.logCompletion(r, "delete", start)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case isNotFound(err):
		a.record(r, "delete", http.StatusNotFound)
		http.NotFound(w, r)
	case isBusy(err):
		a.record(r, "delete", http.StatusConflict)
		http.Error(w, "lifecycle transition in progress, retry shortly", http.StatusConflict)
	default:
		a.record(r, "delete", http.StatusInternalServerError)
		httpx.Log(r.Context(), a.log).Error("lifecycle", "op", "delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (a *API) toggle(w http.ResponseWriter, r *http.Request, op string, run func(ctx context.Context, slug string) (string, error)) {
	start := time.Now()
	if !a.auth(w, r) {
		a.record(r, op, http.StatusUnauthorized)
		return
	}
	status, err := run(r.Context(), r.PathValue("slug"))
	switch {
	case err == nil:
		a.record(r, op, http.StatusOK)
		a.logCompletion(r, op, start)
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	case isNotFound(err):
		a.record(r, op, http.StatusNotFound)
		http.NotFound(w, r)
	case isBusy(err):
		a.record(r, op, http.StatusConflict)
		http.Error(w, "opposite toggle in progress, retry shortly", http.StatusConflict)
	default:
		a.record(r, op, http.StatusInternalServerError)
		httpx.Log(r.Context(), a.log).Error("lifecycle", "op", op, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// logCompletion emits the one structured line per completed lifecycle
// operation (observability: significant admin events emit one line). The
// request-scoped logger contributes request_id and route.
func (a *API) logCompletion(r *http.Request, op string, start time.Time) {
	httpx.Log(r.Context(), a.log).Info("lifecycle",
		"op", op, "slug", r.PathValue("slug"),
		"duration", time.Since(start).String())
}

// auth delegates to the shared admin-plane checker (auth-modes D7).
func (a *API) auth(w http.ResponseWriter, r *http.Request) bool {
	return a.authn.Allow(w, r, auth.KindAPI)
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
func isBusy(err error) bool     { return errors.Is(err, ErrBusy) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
