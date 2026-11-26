package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	walletmigrations "github.com/donaina/echo/db/wallet"
	"github.com/donaina/echo/internal/platform/config"
	"github.com/donaina/echo/internal/platform/httpserver"
	"github.com/donaina/echo/internal/platform/idempotency"
	"github.com/donaina/echo/internal/platform/logging"
	"github.com/donaina/echo/internal/platform/migrate"
	"github.com/donaina/echo/internal/platform/postgres"
	"github.com/donaina/echo/internal/platform/telemetry"
	"github.com/donaina/echo/internal/wallet"
)

// version is stamped at build time:
// go build -ldflags "-X main.version=$(git describe --always --dirty)".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wallet:", err)
		os.Exit(1)
	}
}

func run() error {
	migrateOnly := flag.Bool("migrate", false, "apply the service's migrations and exit")
	flag.Parse()

	r := config.New(os.LookupEnv)
	cfg, err := config.LoadService(r, "wallet", ":8082", ":9082", "WALLET_DATABASE_URL")
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, logging.Options{
		Service: "wallet",
		Version: version,
		Env:     cfg.Env,
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.MaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *migrateOnly {
		if err := migrate.Up(ctx, pool, walletmigrations.Migrations); err != nil {
			return err
		}
		v, err := migrate.Version(ctx, pool)
		if err != nil {
			return err
		}
		logger.Info("migrations applied", "version", v)
		return nil
	}

	// A service refuses to serve a database older than the code it ships with.
	if err := migrate.EnsureCurrent(ctx, pool, walletmigrations.Migrations); err != nil {
		return err
	}

	// The internal API is behind a temporary shared token until Stage 4
	// replaces it with mTLS and a signed caller identity. It has no default.
	serviceToken := r.Required("WALLET_SERVICE_TOKEN")
	internalAddr := r.String("WALLET_INTERNAL_ADDR", ":8182")
	if err := r.Err(); err != nil {
		return err
	}

	tel, err := telemetry.Setup(ctx, telemetry.Options{
		Service: "wallet",
		Version: version,
		Env:     cfg.Env,
	})
	if err != nil {
		return err
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := tel.Shutdown(shutCtx); err != nil {
			logger.Error("telemetry shutdown", "err", err)
		}
	}()

	probes := httpserver.NewProbes(logger)
	probes.Add("postgres", postgres.Ready(pool))
	probes.Ready()

	// The public port serves health and nothing else until Stage 4. The money
	// API lives on the internal port, behind the service token.
	store := wallet.NewStore(pool)
	svc := wallet.NewService(store)
	idemStore := idempotency.NewStore(pool)
	internal := wallet.NewHandler(svc, serviceToken, idempotency.NewMiddleware(idemStore, logger))

	// Two housekeeping loops: expired idempotency keys, and holds whose saga
	// died. Both are safety nets, not the primary path.
	go sweep(ctx, logger, store, idemStore)

	srv := httpserver.New(httpserver.Options{
		Service:         "wallet",
		Addr:            cfg.HTTPAddr,
		InternalAddr:    internalAddr,
		InternalHandler: internal,
		MetricsAddr:     cfg.MetricsAddr,
		Logger:          logger,
		Probes:          probes,
		Metrics:         tel.Metrics,
		Registry:        tel.Registry,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	logger.Info("starting",
		"addr", cfg.HTTPAddr, "internal_addr", internalAddr, "metrics_addr", cfg.MetricsAddr)
	return srv.Run(ctx)
}

func sweep(ctx context.Context, logger *slog.Logger, store *wallet.Store, keys *idempotency.Store) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := store.ExpireHolds(ctx); err != nil {
				logger.Warn("expire holds", "err", err)
			} else if n > 0 {
				logger.Info("expired holds", "count", n)
			}
			if _, err := keys.Sweep(ctx); err != nil {
				logger.Warn("sweep idempotency keys", "err", err)
			}
		}
	}
}
