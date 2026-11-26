package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/donaina/echo/internal/platform/config"
	"github.com/donaina/echo/internal/platform/httpserver"
	"github.com/donaina/echo/internal/platform/logging"
	"github.com/donaina/echo/internal/platform/telemetry"
)

// version is stamped at build time:
// go build -ldflags "-X main.version=$(git describe --always --dirty)".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()

	r := config.New(os.LookupEnv)
	// No database URL: the gateway holds no money state and no session state
	// that survives a request (Article IX).
	cfg, err := config.LoadService(r, "gateway", ":8080", ":9080", "")
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, logging.Options{
		Service: "gateway",
		Version: version,
		Env:     cfg.Env,
		Level:   cfg.LogLevel,
		Format:  cfg.LogFormat,
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel, err := telemetry.Setup(ctx, telemetry.Options{
		Service: "gateway",
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

	// The only public surface. Routes arrive in S4; until then it serves
	// health, readiness and a 404 for everything else.
	probes := httpserver.NewProbes(logger)
	probes.Ready()

	srv := httpserver.New(httpserver.Options{
		Service:         "gateway",
		Addr:            cfg.HTTPAddr,
		MetricsAddr:     cfg.MetricsAddr,
		Handler:         http.NewServeMux(),
		Logger:          logger,
		Probes:          probes,
		Metrics:         tel.Metrics,
		Registry:        tel.Registry,
		DrainDelay:      cfg.DrainDelay,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	logger.Info("starting", "addr", cfg.HTTPAddr, "metrics_addr", cfg.MetricsAddr)
	return srv.Run(ctx)
}
