package upload

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// metricSums returns one counter's data points as map[label-set]value.
func metricSums(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect() err = %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
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

// meteredHandler wraps the standard harness with a manual-reader meter and
// a discard logger so outcomes and lines are observable.
func meteredHandler(t *testing.T, ctx context.Context, logs *strings.Builder) (*Handler, *sdkmetric.ManualReader, *sql.DB) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h, store, pool := newTestHandler(t, ctx, 0, nil)
	h = New(Options{
		DB:    pool,
		Store: store,
		Caps:  h.caps,
		Keep:  h.keep,
		Auth:  h.authn,
		Log:   slog.New(slog.NewTextHandler(logs, nil)),
		Meter: provider.Meter("test"),
	})
	return h, reader, pool
}

func postUpload(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartBody(t, "index.html", []byte("<html>hi</html>"), nil)
	req := httptest.NewRequest("POST", "/api/pages", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.Create().ServeHTTP(rec, req)
	return rec
}

// TestUploadMetrics covers the upload outcome counter (observability D7):
// created, rejected, and failed outcomes each land on their label.
func TestUploadMetrics(t *testing.T) {
	ctx := context.Background()
	var logs strings.Builder
	h, reader, pool := meteredHandler(t, ctx, &logs)

	if rec := postUpload(t, h); rec.Code != http.StatusCreated {
		t.Fatalf("created upload status = %d", rec.Code)
	}

	// rejected: a body that is neither HTML nor zip.
	body, contentType := multipartBody(t, "x.bin", []byte{0x00, 0x01}, nil)
	req := httptest.NewRequest("POST", "/api/pages", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.Create().ServeHTTP(rec, req)
	if rec.Code/100 != 4 {
		t.Fatalf("rejected upload status = %d", rec.Code)
	}

	// failed: the database is closed out from under the handler.
	if err := pool.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if rec := postUpload(t, h); rec.Code/100 != 5 {
		t.Fatalf("failed upload status = %d", rec.Code)
	}

	counts := metricSums(t, reader, "upload.requests")
	for _, want := range []string{"outcome=created", "outcome=rejected", "outcome=failed"} {
		if counts[want] != 1 {
			t.Fatalf("upload.requests[%s] = %d, want 1 (all: %v)", want, counts[want], counts)
		}
	}
}

// TestUploadSuccessLine covers the one-line upload event (observability:
// significant admin events): slug, bytes, and per-status asset counts.
func TestUploadSuccessLine(t *testing.T) {
	ctx := context.Background()
	var logs strings.Builder
	h, _, _ := meteredHandler(t, ctx, &logs)

	if rec := postUpload(t, h); rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", rec.Code)
	}

	out := logs.String()
	for _, want := range []string{"msg=upload", "what=created", "slug=", "bytes=", "assets_local="} {
		if !strings.Contains(out, want) {
			t.Fatalf("upload line missing %q: %s", want, out)
		}
	}
}
