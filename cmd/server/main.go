// Command server boots the static page hosting service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Mode-gated boot (deployment-modes D2): serve mode opens no database
	// resources at all — no pool, no migrations, no bucket creation, no
	// sweep, no upload or lifecycle handlers. Admin/all keep the full boot
	// sequence.
	if cfg.Mode == config.ModeServe {
		return runServe(ctx, cfg)
	}
	return runAdminAll(ctx, cfg)
}

// runServe boots the serving plane straight from the validated storage
// config. The serve path never references a database (deployment-modes D2),
// so its health probes storage instead of Postgres (deployment-modes D4).
func runServe(ctx context.Context, cfg config.Config) error {
	store, err := openStorage(cfg.Storage)
	if err != nil {
		return err
	}
	opts := baseOptions(cfg, store)
	opts.Ping = serve.StorageProbe(store)
	return listen(ctx, cfg, opts)
}

// runAdminAll boots the admin/all planes with the database-backed duties:
// pool, migrations, bucket creation, the lifecycle sweep, and the
// upload/lifecycle handlers. Health stays the Postgres ping
// (deployment-modes D4).
func runAdminAll(ctx context.Context, cfg config.Config) error {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	store, err := openStorage(cfg.Storage)
	if err != nil {
		return err
	}
	if s3, ok := store.(*s3compat.Store); ok {
		if err := s3.EnsureBucket(ctx); err != nil {
			return err
		}
	}

	// Resume any toggle a crash left mid-flight before serving traffic.
	lc := lifecycle.New(pool, store)
	if err := lc.Sweep(ctx); err != nil {
		return err
	}

	api := upload.New(upload.Options{
		Pool:  pool,
		Store: store,
		Caps:  cfg.Caps,
		Keep: ingest.KeepRules{
			Fonts: cfg.KeepExternal.Fonts, JS: cfg.KeepExternal.JS,
			Icons: cfg.KeepExternal.Icons, Misc: cfg.KeepExternal.Misc,
		},
		Token: cfg.AuthToken,
	})

	opts := baseOptions(cfg, store)
	opts.Upload = api
	opts.Lifecycle = lifecycle.NewAPI(lc, cfg.AuthToken)
	opts.Ping = pool.Ping
	return listen(ctx, cfg, opts)
}

// baseOptions is the serve.Options prefix every mode shares: the plane
// selection, the storage backend, and the entry-cache TTL. Mode-specific
// options (health probe, upload/lifecycle handlers) are set by each boot
// path on top of it.
func baseOptions(cfg config.Config, store storage.Storage) serve.Options {
	return serve.Options{
		Mode:     cfg.Mode,
		Store:    store,
		CacheTTL: cfg.CacheTTL,
	}
}

// listen runs the router under the shared signal handling: serve until the
// context is canceled, then shut down gracefully.
func listen(ctx context.Context, cfg config.Config, opts serve.Options) error {
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           serve.New(opts),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", cfg.Addr, "mode", cfg.Mode, "driver", cfg.Storage.Driver)

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
