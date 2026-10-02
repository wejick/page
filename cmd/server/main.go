// Command server boots the static page hosting service.
package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"

	_ "modernc.org/sqlite"

	"page/internal/auth"
	"page/internal/config"
	"page/internal/db"
	"page/internal/ingest"
	"page/internal/lifecycle"
	"page/internal/serve"
	"page/internal/storage"
	"page/internal/storage/mem"
	"page/internal/storage/s3compat"
	"page/internal/upload"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadEnv()
	if err != nil {
		return err
	}

	// Logging and metrics come up before anything else, so the boot trail
	// itself is visible (observability D1, D2, D5).
	log := newJSONLogger(os.Stdout, cfg.LogLevel)
	meterProvider := mustSetupMetrics(cfg, log)
	defer func() {
		// Flush the last metrics interval on the way out; a failed flush is
		// warned about, never fatal (observability D8).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := meterProvider.Shutdown(ctx); err != nil {
			log.Warn("metrics", "what", "shutdown flush", "err", err)
		}
	}()
	logBootSummary(log, cfg)
	log.Info("boot", "what", "version", "version", serve.ModuleVersion())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Mode-gated boot (deployment-modes D2): serve mode opens no database
	// resources at all — no pool, no migrations, no bucket creation, no
	// sweep, no upload or lifecycle handlers. Admin/all keep the full boot
	// sequence.
	if cfg.Mode == config.ModeServe {
		return runServe(ctx, cfg, log, meterProvider)
	}
	return runAdminAll(ctx, cfg, log, meterProvider)
}

// logBootSummary echoes the running configuration with secrets reduced to
// presence markers (observability D2: no token or secret value is logged).
func logBootSummary(log *slog.Logger, cfg config.Config) {
	log.Info("boot", "what", "config",
		"mode", cfg.Mode, "addr", cfg.Addr,
		"storage_driver", cfg.Storage.Driver, "bucket", cfg.Storage.Bucket,
		"auth_mode", cfg.AuthMode,
		"auth_token", setOrUnset(cfg.AuthToken),
		"session_secret", setOrUnset(cfg.SessionSecret),
		"cache_ttl", cfg.CacheTTL.String(),
		"otel_endpoint", setOrUnset(cfg.OTelEndpoint))
}

// bootStep times one named boot step and logs its completion; failures are
// returned (and logged once as the fatal error), never double-logged.
func bootStep(ctx context.Context, log *slog.Logger, name string, fn func(context.Context) error) error {
	start := time.Now()
	if err := fn(ctx); err != nil {
		return err
	}
	log.Info("boot", "step", name, "duration", time.Since(start).String())
	return nil
}

// runServe boots the serving plane straight from the validated storage
// config. The serve path never references a database (deployment-modes D2),
// so its health probes storage instead of the database (deployment-modes D4).
func runServe(ctx context.Context, cfg config.Config, log *slog.Logger, mp *metric.MeterProvider) error {
	store, err := openStorage(cfg.Storage)
	if err != nil {
		return err
	}
	opts := baseOptions(cfg, store, log, mp)
	opts.Ping = serve.StorageProbe(store)
	return listen(ctx, cfg, log, opts)
}

// runAdminAll boots the admin/all planes with the database-backed duties:
// open the SQLite file (the Litestream sidecar replicates it; restore-if-
// absent is a pre-boot step owned by the sidecar), migrations, bucket
// creation, the lifecycle sweep, and the upload/lifecycle handlers. Health
// stays the database ping (deployment-modes D4).
func runAdminAll(ctx context.Context, cfg config.Config, log *slog.Logger, mp *metric.MeterProvider) error {
	var pool *sql.DB
	if err := bootStep(ctx, log, "database open", func(ctx context.Context) error {
		p, err := db.Open(cfg.SQLitePath)
		if err != nil {
			return err
		}
		pool = p
		return pool.PingContext(ctx)
	}); err != nil {
		return err
	}
	defer pool.Close()
	if err := bootStep(ctx, log, "migrate", func(ctx context.Context) error {
		return db.Migrate(ctx, pool)
	}); err != nil {
		return err
	}

	var store storage.Storage
	if err := bootStep(ctx, log, "storage open", func(ctx context.Context) error {
		s, err := openStorage(cfg.Storage)
		if err != nil {
			return err
		}
		store = s
		return nil
	}); err != nil {
		return err
	}
	if s3, ok := store.(*s3compat.Store); ok {
		if err := bootStep(ctx, log, "bucket create", s3.EnsureBucket); err != nil {
			return err
		}
	}
	store = instrumentStorage(store, mp)

	// Resume any toggle a crash left mid-flight before serving traffic.
	lc := lifecycle.New(log, pool, store)
	if err := bootStep(ctx, log, "lifecycle sweep", lc.Sweep); err != nil {
		return err
	}

	// Admin-plane auth per AUTH_MODE (auth-modes D1/D7). In oidc mode the
	// boot runs discovery, so a misconfigured IdP fails startup here.
	var oidcFlow *auth.OIDC
	if cfg.AuthMode == config.AuthModeOIDC {
		if err := bootStep(ctx, log, "oidc discovery", func(ctx context.Context) error {
			discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			flow, err := auth.NewOIDC(discoveryCtx, cfg.OIDC, cfg.SessionSecret, log)
			if err != nil {
				return err
			}
			oidcFlow = flow
			return nil
		}); err != nil {
			return err
		}
	}
	checker := auth.NewChecker(cfg.AuthMode, cfg.AuthToken, oidcFlow)
	if cfg.AuthMode == config.AuthModeNone {
		log.Warn("admin plane is unauthenticated (AUTH_MODE=none); " +
			"network- or proxy-level protection is required")
	}

	api := upload.New(upload.Options{
		DB:    pool,
		Store: store,
		Caps:  cfg.Caps,
		Keep: ingest.KeepRules{
			Fonts: cfg.KeepExternal.Fonts, JS: cfg.KeepExternal.JS,
			Icons: cfg.KeepExternal.Icons, Misc: cfg.KeepExternal.Misc,
		},
		Auth:  checker,
		Log:   log,
		Meter: mp.Meter("page"),
	})

	opts := baseOptions(cfg, store, log, mp)
	opts.Upload = api
	opts.Lifecycle = lifecycle.NewAPI(lc, checker, log, mp.Meter("page"))
	opts.Auth = checker
	opts.Ping = func(ctx context.Context) error { return db.Ping(ctx, pool) }
	return listen(ctx, cfg, log, opts)
}

// baseOptions is the serve.Options prefix every mode shares: the plane
// selection, the storage backend, the entry-cache TTL, and the observability
// handles. Mode-specific options (health probe, upload/lifecycle handlers)
// are set by each boot path on top of it.
func baseOptions(cfg config.Config, store storage.Storage, log *slog.Logger, mp *metric.MeterProvider) serve.Options {
	return serve.Options{
		Mode:     cfg.Mode,
		Store:    store,
		CacheTTL: cfg.CacheTTL,
		Log:      log,
		Meter:    mp.Meter("page"),
	}
}

// listen runs the router under the shared signal handling: serve until the
// context is canceled, then shut down gracefully.
func listen(ctx context.Context, cfg config.Config, log *slog.Logger, opts serve.Options) error {
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           serve.New(opts),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Addr, "mode", cfg.Mode, "driver", cfg.Storage.Driver)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}

// newJSONLogger builds the process logger: JSON, the given sink and level
// (observability D1). The logger is threaded explicitly; the global slog
// default stays untouched.
func newJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// mustSetupMetrics builds the OTLP meter provider from the validated
// config (observability D5): endpoint unset → a reader-less provider
// (recording is inert, nothing exports), set → periodic OTLP export in the
// background. The global otel error handler is capped at warn so exporter
// failures never log louder than the serving plane (D8).
func mustSetupMetrics(cfg config.Config, log *slog.Logger) *metric.MeterProvider {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("metrics", "what", "otel error handler", "err", err)
	}))
	if cfg.OTelEndpoint == "" {
		return metric.NewMeterProvider()
	}
	exp, err := otlpmetrichttp.New(context.Background())
	if err != nil {
		// Config validated the env; a still-unbuildable exporter must not
		// fail the boot (observability D8).
		log.Warn("metrics", "what", "otlp exporter unavailable, metrics disabled", "err", err)
		return metric.NewMeterProvider()
	}
	res, err := resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName("page"),
			semconv.ServiceVersion(serve.ModuleVersion())))
	if err != nil {
		log.Warn("metrics", "what", "resource build", "err", err)
	}
	return metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(exp)),
		metric.WithResource(res))
}

// instrumentStorage wraps the storage seam with the op/outcome decorator
// (observability D6). Metrics failing to build never fail the boot.
func instrumentStorage(store storage.Storage, mp *metric.MeterProvider) storage.Storage {
	inst, err := storage.Instrument(store, mp.Meter("page"))
	if err != nil {
		slog.Warn("metrics", "what", "storage instrumentation unavailable", "err", err)
		return store
	}
	return inst
}

// setOrUnset reveals whether a secret is configured without revealing it.
func setOrUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return "(set)"
}

func openStorage(cfg config.Storage) (storage.Storage, error) {
	switch cfg.Driver {
	case "mem":
		return mem.New(), nil
	case "s3compat":
		return s3compat.New(cfg)
	default:
		return nil, errors.New("unknown storage driver: " + cfg.Driver)
	}
}
