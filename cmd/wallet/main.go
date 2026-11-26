package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	walletmigrations "github.com/donaina/echo/db/wallet"
	"github.com/donaina/echo/internal/platform/config"
	"github.com/donaina/echo/internal/platform/httpserver"
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
	svc := wallet.NewService(wallet.NewStore(pool))
	internal := wallet.NewHandler(svc, serviceToken)

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
