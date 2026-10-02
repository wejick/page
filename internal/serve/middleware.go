package serve

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"page/internal/httpx"
)

// requestRecorder captures the status code and byte count of one response
// for the access log and the route metrics. The route pattern is written
// back onto it by withRoute, because the inner handler's context additions
// are not visible to the middleware's local context.
type requestRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	route  string
}

func (r *requestRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *requestRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *requestRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// context keys private to the serve package: the recorder and the matched
// route pattern, both set by the middleware chain below.
type ctxKey int

const (
	recKey ctxKey = iota
	routeKey
)

// instrumentation holds the process-wide metric instruments (observability
// D4, D5): HTTP counters/histograms labeled by route pattern only.
type instrumentation struct {
	httpReqs metric.Int64Counter     // http.server.requests: route, status
	httpDur  metric.Float64Histogram // http.server.duration: route
}

func newInstrumentation(m metric.Meter) instrumentation {
	// Instrument names are compile-time constants; a failure means a broken
	// meter, and degrading to noop instruments beats failing the boot
	// (observability D8: metrics never gate serving).
	reqs, err := m.Int64Counter("http.server.requests",
		metric.WithDescription("HTTP requests by route pattern and status"))
	if err != nil {
		m = noop.NewMeterProvider().Meter("page")
		reqs, _ = m.Int64Counter("http.server.requests")
	}
	dur, _ := m.Float64Histogram("http.server.duration",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithUnit("s"))
	return instrumentation{httpReqs: reqs, httpDur: dur}
}

// instrument wraps the mux with the full per-request chain (observability
// D3): request ID assignment, panic recovery, and the access log. Route
// tagging and the route metrics live on the inner registrations (withRoute),
// so the access line and metrics can both see the matched pattern.
func (h *Handler) instrument(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Request ID (observability D3): honor a client-supplied ID when it
		// is a sane header value, otherwise mint one.
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = httpx.NewRequestID()
		}
		w.Header().Set("X-Request-ID", id)

		rec := &requestRecorder{ResponseWriter: w}
		ctx := httpx.WithRequestID(r.Context(), id)
		ctx = httpx.WithLog(ctx, h.log.With("request_id", id))
		ctx = context.WithValue(ctx, recKey, rec)
		r = r.WithContext(ctx)

		// LIFO: panic recovery unwinds first (writing the 500), then the
		// access line records the final status. The context logger carries
		// request_id and route (stamped above and by withRoute).
		defer func() {
			level := slog.LevelInfo
			if r.URL.Path == "/healthz" {
				level = slog.LevelDebug // probe noise stays out of info
			}
			httpx.Log(ctx, h.log).Log(ctx, level, "access",
				"method", r.Method, "route", rec.route, "path", r.URL.Path,
				"status", rec.status, "duration", time.Since(start).String(),
				"bytes", rec.bytes)
		}()
		defer func() {
			if p := recover(); p != nil {
				// The context logger carries request_id and route.
				httpx.Log(ctx, h.log).ErrorContext(ctx, "panic",
					"method", r.Method, "path", r.URL.Path,
					"panic", p, "stack", string(debug.Stack()))
				if rec.status == 0 {
					http.Error(w, "internal error", http.StatusInternalServerError)
				}
			}
		}()

		mux.ServeHTTP(rec, r)
	})
}

// validRequestID accepts only bounded printable-ASCII IDs: header values are
// echoed into logs, so they must not smuggle control characters or grow
// unbounded.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// withRoute is the registration wrapper (observability D4): it tags the
// request with its route pattern and records the route-labeled metrics.
// Labels are always the pattern — slugs and raw paths never become metric
// labels; they stay in the access log.
func (h *Handler) withRoute(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if rec, ok := w.(*requestRecorder); ok {
			rec.route = pattern
		}
		ctx := context.WithValue(r.Context(), routeKey, pattern)
		inner := httpx.Log(ctx, h.log).With("route", pattern)
		ctx = httpx.WithLog(ctx, inner)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)

		status := http.StatusInternalServerError
		if rec, ok := ctx.Value(recKey).(*requestRecorder); ok && rec.status != 0 {
			status = rec.status
		} else if ok && rec.status == 0 {
			status = http.StatusOK
		}
		attrs := metric.WithAttributes(
			attribute.String("route", pattern),
			attribute.Int("status", status))
		h.instr.httpReqs.Add(r.Context(), 1, attrs)
		h.instr.httpDur.Record(r.Context(), time.Since(start).Seconds(), attrs)
	})
}
