package lifecycle

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"page/internal/auth"
	"page/internal/config"
	"page/internal/db"
	"page/internal/storage/mem"
)

// metricsHarness builds a lifecycle API over real temp SQLite + mem storage
// with a manual-reader meter, one live page, and a captured logger.
type metricsHarness struct {
	api    *API
	reader *sdkmetric.ManualReader
	logs   *strings.Builder
}

func newMetricsHarness(t *testing.T) *metricsHarness {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := mem.New()
	if err := store.Put(ctx, "demo/index.html", "text/html", strings.NewReader("<html/>")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.CreatePage(ctx, d, db.PageRecord{Slug: "demo", Identifier: "demo", Code: 1}, nil, 8); err != nil {
		t.Fatalf("create page: %v", err)
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))
	svc := New(log, d, store)
	api := NewAPI(svc, auth.NewChecker(config.AuthModeToken, "secret", nil), log, provider.Meter("test"))
	return &metricsHarness{api: api, reader: reader, logs: &logs}
}

func lifecycleCounts(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() err = %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "lifecycle.ops" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				var parts []string
				for _, kv := range dp.Attributes.ToSlice() {
					parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
				}
				out[strings.Join(parts, ",")] += dp.Value
			}
		}
	}
	return out
}

// TestLifecycleMetrics covers the op × outcome counter and the completion
// line (observability D7): park lands ok, an unknown slug lands not_found,
// and each success emits its structured line.
func TestLifecycleMetrics(t *testing.T) {
	h := newMetricsHarness(t)

	// park ok
	req := httptest.NewRequest("POST", "/park", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.SetPathValue("slug", "demo")
	rec := httptest.NewRecorder()
	h.api.Park().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("park status = %d", rec.Code)
	}

	// park unknown slug → not_found
	req = httptest.NewRequest("POST", "/park", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.SetPathValue("slug", "missing")
	rec = httptest.NewRecorder()
	h.api.Park().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("park unknown slug status = %d", rec.Code)
	}

	counts := lifecycleCounts(t, h.reader)
	if counts["op=park,outcome=ok"] != 1 || counts["op=park,outcome=not_found"] != 1 {
		t.Fatalf("lifecycle.ops = %v", counts)
	}

	out := h.logs.String()
	if !strings.Contains(out, "msg=lifecycle") || !strings.Contains(out, "op=park") ||
		!strings.Contains(out, "slug=demo") || !strings.Contains(out, "duration=") {
		t.Fatalf("park completion line incomplete: %s", out)
	}
}
