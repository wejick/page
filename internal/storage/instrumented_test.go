package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"page/internal/storage"
	"page/internal/storage/mem"
	"page/internal/storage/storagetest"
)

// newInstrumented builds a decorator over mem with a manual reader so tests
// can assert the recorded label sets exactly.
func newInstrumented(t *testing.T) (*storage.Instrumented, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	inst, err := storage.Instrument(mem.New(), provider.Meter("test"))
	if err != nil {
		t.Fatalf("Instrument() err = %v", err)
	}
	return inst, reader
}

// collect returns the recorded data points for one metric as
// map[labelSet]value.
func collect(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]int64 {
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
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is not an int64 sum", name)
			}
			for _, dp := range sum.DataPoints {
				var labels []string
				for _, a := range sortedAttrs(dp.Attributes) {
					labels = append(labels, string(a.Key)+"="+a.Value.Emit())
				}
				out[strings.Join(labels, ",")] += dp.Value
			}
		}
	}
	return out
}

func sortedAttrs(set attribute.Set) []attribute.KeyValue {
	kvs := set.ToSlice()
	for i := 1; i < len(kvs); i++ {
		for j := i; j > 0 && kvs[j].Key < kvs[j-1].Key; j-- {
			kvs[j], kvs[j-1] = kvs[j-1], kvs[j]
		}
	}
	return kvs
}

// TestInstrumentedConformance runs the frozen seam conformance suite through
// the decorator: wrapping must not change observable behavior.
func TestInstrumentedConformance(t *testing.T) {
	inst, _ := newInstrumented(t)
	storagetest.Run(t, context.Background(), inst)
}

func TestInstrumentedMetrics(t *testing.T) {
	ctx := context.Background()
	inst, reader := newInstrumented(t)

	// ok outcome
	if err := inst.Put(ctx, "a/index.html", "text/html", strings.NewReader("x")); err != nil {
		t.Fatalf("Put() err = %v", err)
	}
	if _, err := inst.Stat(ctx, "a/index.html"); err != nil {
		t.Fatalf("Stat() err = %v", err)
	}
	// not_found outcome
	_, err := inst.Get(ctx, "a/missing.html")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get() err = %v, want ErrNotFound", err)
	}
	// error outcome
	if err := inst.DeletePrefix(ctx, "a/"); err != nil {
		t.Fatalf("DeletePrefix() err = %v", err)
	}

	got := collect(t, reader, "storage.ops")
	want := map[string]int64{
		"op=put,outcome=ok":           1,
		"op=stat,outcome=ok":          1,
		"op=get,outcome=not_found":    1,
		"op=delete_prefix,outcome=ok": 1,
	}
	for labels, count := range want {
		if got[labels] != count {
			t.Fatalf("storage.ops[%s] = %d, want %d (all: %v)", labels, got[labels], count, got)
		}
		delete(got, labels)
	}
	for labels := range got {
		t.Errorf("unexpected storage.ops label set %q", labels)
	}
}
