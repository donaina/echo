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

	transfermigrations "github.com/donaina/echo/db/transfer"
	"github.com/donaina/echo/internal/platform/config"
	"github.com/donaina/echo/internal/platform/httpserver"
	"github.com/donaina/echo/internal/platform/idempotency"
	"github.com/donaina/echo/internal/platform/logging"
	"github.com/donaina/echo/internal/platform/migrate"
	"github.com/donaina/echo/internal/platform/postgres"
	"github.com/donaina/echo/internal/platform/telemetry"
	"github.com/donaina/echo/internal/transfer"
	"github.com/donaina/echo/internal/transfer/walletclient"
)

// version is stamped at build time:
// go build -ldflags "-X main.version=$(git describe --always --dirty)".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "transfer:", err)
		os.Exit(1)
	}
}

func run() error {
	migrateOnly := flag.Bool("migrate", false, "apply the service's migrations and exit")
	flag.Parse()

	r := config.New(os.LookupEnv)
	cfg, err := config.LoadService(r, "transfer", ":8083", ":9083", "TRANSFER_DATABASE_URL")
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, logging.Options{
		Service: "transfer",
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
		if err := migrate.Up(ctx, pool, transfermigrations.Migrations); err != nil {
			return err
		}
		v, err := migrate.Version(ctx, pool)
		if err != nil {
			return err
		}
		logger.Info("migrations applied", "version", v)
		return nil
	}

	if err := migrate.EnsureCurrent(ctx, pool, transfermigrations.Migrations); err != nil {
		return err
	}

	// The inbound token guards this service's API; the wallet token is what
	// it presents when it calls the wallet. Both are required: a service that
	// cannot authenticate its calls has no safe default.
	serviceToken := r.Required("TRANSFER_SERVICE_TOKEN")
	walletToken := r.Required("WALLET_SERVICE_TOKEN")
	walletURL := r.Required("WALLET_URL")
	internalAddr := r.String("TRANSFER_INTERNAL_ADDR", ":8183")
	if err := r.Err(); err != nil {
		return err
	}

	wallet, err := walletclient.New(walletURL, walletToken)
	if err != nil {
		return err
	}

	tel, err := telemetry.Setup(ctx, telemetry.Options{
		Service: "transfer",
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

	store := transfer.NewStore(pool)
	orchestrator := transfer.NewOrchestrator(store, wallet, transfer.NewStubDestination(), logger)
	go orchestrator.Run(ctx)

	idemStore := idempotency.NewStore(pool)
	svc := transfer.NewService(store, orchestrator.Wake)
	internal := transfer.NewHandler(svc, serviceToken, idempotency.NewMiddleware(idemStore, logger))
	go sweep(ctx, logger, idemStore)

	srv := httpserver.New(httpserver.Options{
		Service:         "transfer",
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
		"addr", cfg.HTTPAddr, "internal_addr", internalAddr,
		"wallet", walletURL, "metrics_addr", cfg.MetricsAddr)
	return srv.Run(ctx)
}

func sweep(ctx context.Context, logger *slog.Logger, keys *idempotency.Store) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := keys.Sweep(ctx); err != nil {
				logger.Warn("sweep idempotency keys", "err", err)
			}
		}
	}
}
