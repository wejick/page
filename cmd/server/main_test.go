package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/sdk/metric"

	"page/internal/config"
)

// logLines splits captured JSON log output into decoded records.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestNewJSONLogger covers the logging foundation (observability D1): JSON
// records with time/level/msg on the configured sink, and debug suppressed
// at info level.
func TestNewJSONLogger(t *testing.T) {
	var buf bytes.Buffer
	log := newJSONLogger(&buf, slog.LevelInfo)
	log.Info("hello", "k", "v")
	log.Debug("hidden")

	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (debug must be suppressed at info)", len(lines))
	}
	rec := lines[0]
	for _, key := range []string{"time", "level", "msg", "k"} {
		if _, ok := rec[key]; !ok {
			t.Fatalf("log record missing %q: %v", key, rec)
		}
	}
	if rec["level"] != "INFO" || rec["msg"] != "hello" {
		t.Fatalf("record = %v", rec)
	}
}

// TestBootTrail boots a full admin/all instance against mem storage and a
// temp SQLite file and asserts the boot trail (observability: boot steps)
// and that the config summary never leaks secrets (observability D2).
func TestBootTrail(t *testing.T) {
	var buf bytes.Buffer
	log := newJSONLogger(&buf, slog.LevelInfo)

	dbPath := filepath.Join(t.TempDir(), "page.db")
	cfg := config.Config{
		Mode:       config.ModeAll,
		Addr:       ":0", // random port: the test server must not collide
		SQLitePath: dbPath,
		AuthMode:   config.AuthModeToken,
		AuthToken:  "supersecret-token-value",
		CacheTTL:   time.Second,
		Storage:    config.Storage{Driver: "mem"},
	}

	// Boot, let the server listen briefly, then shut down gracefully.
	logBootSummary(log, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	if err := runAdminAll(ctx, cfg, log, metric.NewMeterProvider()); err != nil {
		t.Fatalf("runAdminAll() err = %v", err)
	}

	lines := logLines(t, &buf)
	var steps []string
	var sawListening bool
	for _, rec := range lines {
		if rec["msg"] == "boot" {
			if step, ok := rec["step"].(string); ok {
				steps = append(steps, step)
			}
		}
		if rec["msg"] == "listening" {
			sawListening = true
		}
	}
	for _, want := range []string{"database open", "migrate", "storage open", "lifecycle sweep"} {
		found := false
		for _, s := range steps {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("boot trail missing step %q; steps = %v", want, steps)
		}
	}
	if !sawListening {
		t.Fatal("boot trail missing the listening line")
	}

	// Config summary echoes state, never secret values.
	if strings.Contains(buf.String(), "supersecret-token-value") {
		t.Fatal("auth token value leaked into the boot log")
	}
	if !strings.Contains(buf.String(), "(set)") {
		t.Fatal("config summary should mark the configured token as (set)")
	}
}

// TestOTLPFlushOnShutdown covers the export path (observability D5): a
// provider wired to an OTLP endpoint delivers metric data when shut down,
// so the last interval is not dropped.
func TestOTLPFlushOnShutdown(t *testing.T) {
	var exports atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	exp, err := otlpmetrichttp.New(context.Background(), otlpmetrichttp.WithEndpointURL(ts.URL))
	if err != nil {
		t.Fatalf("otlpmetrichttp.New() err = %v", err)
	}
	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(exp, metric.WithInterval(time.Hour))))
	counter, err := mp.Meter("test").Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("Int64Counter() err = %v", err)
	}
	counter.Add(context.Background(), 1)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mp.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() err = %v", err)
	}
	if exports.Load() == 0 {
		t.Fatal("shutdown did not flush any export to the collector")
	}
}
