package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"page/internal/httpx"
	"page/internal/storage/mem"
)

// obsHarness builds a full router over mem storage with a captured log and
// a manual-reader meter, plus one seeded page.
type obsHarness struct {
	ts     *httptest.Server
	logs   *bytes.Buffer
	reader *sdkmetric.ManualReader
}

func newObsHarness(t *testing.T) *obsHarness {
	t.Helper()
	var buf bytes.Buffer
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	store := mem.New()
	if err := store.Put(context.Background(), "demo/index.html",
		"text/html", strings.NewReader("<html>demo</html>")); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	h := New(Options{
		Store: store,
		Log:   slog.New(slog.NewJSONHandler(&buf, nil)),
		Meter: provider.Meter("test"),
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &obsHarness{ts: ts, logs: &buf, reader: reader}
}

func (o *obsHarness) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(o.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// accessLines returns only the "access" records.
func accessLines(t *testing.T, recs []map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range recs {
		if rec["msg"] == "access" {
			out = append(out, rec)
		}
	}
	return out
}

func get(t *testing.T, url string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

// TestRequestID covers the request-ID contract (observability: request IDs
// on every request): a client-supplied ID is honored end to end, an absent
// one is minted, and both land in the access line and response header.
func TestRequestID(t *testing.T) {
	o := newObsHarness(t)

	resp, _ := get(t, o.ts.URL+"/p/demo/", map[string]string{"X-Request-ID": "abc123"})
	if got := resp.Header.Get("X-Request-ID"); got != "abc123" {
		t.Fatalf("echoed request ID = %q, want abc123", got)
	}

	resp2, _ := get(t, o.ts.URL+"/p/demo/", nil)
	minted := resp2.Header.Get("X-Request-ID")
	if len(minted) != 32 {
		t.Fatalf("minted request ID = %q, want 32 hex chars", minted)
	}

	lines := accessLines(t, o.records(t))
	var sawSupplied, sawMinted bool
	for _, rec := range lines {
		switch rec["request_id"] {
		case "abc123":
			sawSupplied = true
		case minted:
			sawMinted = true
		}
	}
	if !sawSupplied || !sawMinted {
		t.Fatalf("access lines missing request IDs (supplied=%v minted=%v)", sawSupplied, sawMinted)
	}
}

// TestRequestIDRejectsHostileHeader: oversized or control-bearing IDs are
// replaced, not echoed.
func TestRequestIDRejectsHostileHeader(t *testing.T) {
	o := newObsHarness(t)
	hostile := strings.Repeat("x", 129)
	resp, _ := get(t, o.ts.URL+"/p/demo/", map[string]string{"X-Request-ID": hostile})
	if got := resp.Header.Get("X-Request-ID"); got == hostile {
		t.Fatal("oversized client ID was echoed verbatim")
	}
	if len(resp.Header.Get("X-Request-ID")) != 32 {
		t.Fatal("hostile ID was not replaced with a minted one")
	}
}

// TestAccessLog covers the one-line-per-request contract (observability:
// access log) and the route-pattern labels (D4).
func TestAccessLog(t *testing.T) {
	o := newObsHarness(t)

	get(t, o.ts.URL+"/p/demo/", nil)
	get(t, o.ts.URL+"/p/nope/", nil) // 404
	get(t, o.ts.URL+"/healthz", nil) // debug-only at info level

	lines := accessLines(t, o.records(t))
	var page, missing bool
	for _, rec := range lines {
		switch {
		case rec["path"] == "/p/demo/":
			page = true
			if rec["route"] != "GET /p/{slug}/{$}" || rec["status"] != float64(200) {
				t.Fatalf("page access line = %v", rec)
			}
			if _, ok := rec["duration"]; !ok {
				t.Fatal("access line missing duration")
			}
			if _, ok := rec["bytes"]; !ok {
				t.Fatal("access line missing bytes")
			}
		case rec["path"] == "/p/nope/":
			missing = true
			if rec["status"] != float64(404) {
				t.Fatalf("404 access line status = %v", rec["status"])
			}
		}
		if rec["path"] == "/healthz" {
			t.Fatalf("healthz logged at info level: %v", rec)
		}
	}
	if !page || !missing {
		t.Fatalf("access lines incomplete (page=%v missing=%v)", page, missing)
	}
}

// TestAccessLogDebugShowsHealthz: at debug level, healthz requests appear.
func TestAccessLogDebugShowsHealthz(t *testing.T) {
	var buf bytes.Buffer
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h := New(Options{
		Store: mem.New(),
		Log:   slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Meter: provider.Meter("test"),
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	get(t, ts.URL+"/healthz", nil)

	found := false
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `"path":"/healthz"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("healthz access line missing at debug level")
	}
}

// TestPanicRecovery covers the recovery middleware (observability: panic
// recovery): a panicking handler yields a 500, not a dropped connection,
// with the panic value, stack, and request ID in the log.
func TestPanicRecovery(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	meter := sdkmetric.NewMeterProvider().Meter("test")

	h := &Handler{log: log, instr: newInstrumentation(meter),
		cache: newHTMLCache(0, 0, meter), startedAt: time.Now(), version: "test"}
	mux := http.NewServeMux()
	mux.Handle("GET /boom", h.withRoute("GET /boom", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom-value")
	})))
	ts := httptest.NewServer(h.instrument(mux))
	defer ts.Close()

	resp, _ := get(t, ts.URL+"/boom", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", resp.StatusCode)
	}
	out := buf.String()
	for _, want := range []string{"boom-value", "panic", "request_id"} {
		if !strings.Contains(out, want) {
			t.Fatalf("panic log missing %q: %s", want, out)
		}
	}
	if !strings.Contains(out, "runtime/debug") && !strings.Contains(out, "panic_test") {
		if !strings.Contains(out, "goroutine") {
			t.Fatalf("panic log missing stack trace: %s", out)
		}
	}
}

// TestServeErrorCarriesRequestContext covers error-log context (D3): the
// 500's error line includes the request ID and route of the affected
// request.
func TestServeErrorCarriesRequestContext(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := &Handler{log: log, instr: newInstrumentation(sdkmetric.NewMeterProvider().Meter("test")),
		cache: newHTMLCache(0, 0, sdkmetric.NewMeterProvider().Meter("test"))}

	req := httptest.NewRequest("GET", "/p/demo/", nil)
	ctx := httpx.WithRequestID(req.Context(), "req-42")
	ctx = httpx.WithLog(ctx, log.With("request_id", "req-42").With("route", "GET /p/{slug}/{$}"))
	h.serveError(httptest.NewRecorder(), req.WithContext(ctx), errors.New("transport down"))

	out := buf.String()
	if !strings.Contains(out, `"request_id":"req-42"`) {
		t.Fatalf("serve error line missing request ID: %s", out)
	}
	if !strings.Contains(out, `"route":"GET /p/{slug}/{$}"`) {
		t.Fatalf("serve error line missing route: %s", out)
	}
	if !strings.Contains(out, "transport down") {
		t.Fatalf("serve error line missing the error: %s", out)
	}
}

// metricCounts returns the int64 sum data points of one metric as
// map[label-set]count.
func metricCounts(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() err = %v", err)
	}
	out := map[string]float64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch sum := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range sum.DataPoints {
					var parts []string
					for _, kv := range dp.Attributes.ToSlice() {
						parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
					}
					out[strings.Join(parts, ",")] += float64(dp.Value)
				}
			case metricdata.Sum[float64]:
				for _, dp := range sum.DataPoints {
					var parts []string
					for _, kv := range dp.Attributes.ToSlice() {
						parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
					}
					out[strings.Join(parts, ",")] += dp.Value
				}
			}
		}
	}
	return out
}

// TestHTTPMetricsCardinality covers D4's cardinality rule: requests to any
// number of distinct slugs stay bounded by the route-pattern label set. The
// demo page answers 200; the rest 404 — all carry the same route pattern,
// none leak the slug into labels.
func TestHTTPMetricsCardinality(t *testing.T) {
	o := newObsHarness(t)

	get(t, o.ts.URL+"/p/demo/", nil) // 200
	for _, slug := range []string{"a", "b", "c", "d"} {
		get(t, o.ts.URL+"/p/"+slug+"/", nil) // 404, same route pattern
	}

	counts := metricCounts(t, o.reader, "http.server.requests")
	var total float64
	for labels, n := range counts {
		total += n
		if strings.Contains(labels, "slug=") {
			t.Fatalf("raw slug leaked into metric labels: %q", labels)
		}
		if !strings.HasPrefix(labels, "route=GET /p/{slug}/{$}") {
			t.Fatalf("unexpected label set: %q", labels)
		}
	}
	if total != 5 {
		t.Fatalf("recorded %v request datapoints, want 5", total)
	}
}

// TestHTMLCacheMetrics covers the cache outcome counters (observability
// D7): repeated requests record miss then hit.
func TestHTMLCacheMetrics(t *testing.T) {
	o := newObsHarness(t)

	get(t, o.ts.URL+"/p/demo/", nil) // miss
	get(t, o.ts.URL+"/p/demo/", nil) // hit

	counts := metricCounts(t, o.reader, "html.cache")
	if counts["outcome=miss"] != 1 {
		t.Fatalf("html.cache miss = %v, want 1", counts)
	}
	if counts["outcome=hit"] != 1 {
		t.Fatalf("html.cache hit = %v, want 1", counts)
	}
}

// TestHealthzBuildInfo covers the health response contract: JSON body with
// version and uptime, status semantics unchanged.
func TestHealthzBuildInfo(t *testing.T) {
	o := newObsHarness(t)

	resp, raw := get(t, o.ts.URL+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("healthz body is not JSON: %v (%q)", err, raw)
	}
	if body["status"] != "ok" {
		t.Fatalf("healthz status = %v", body["status"])
	}
	if v, ok := body["version"].(string); !ok || v == "" {
		t.Fatalf("healthz version = %v, want non-empty string", body["version"])
	}
	if _, ok := body["uptime"].(string); !ok {
		t.Fatalf("healthz uptime = %v, want string", body["uptime"])
	}
}
