// Package telemetry wires OpenTelemetry: traces to an OTLP collector when one
// is configured, metrics to the Prometheus registry the service exposes on its
// internal port. Propagation always reads and writes W3C traceparent.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlptrace "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Options identifies the service in every span and metric.
type Options struct {
	Service string
	Version string
	Env     string
}

// Telemetry owns the providers and the registry a service shuts down last.
type Telemetry struct {
	Registry *prometheus.Registry
	Metrics  http.Handler
	tracer   trace.Tracer
	shutdown []func(context.Context) error
}

// Setup installs the global tracer and meter providers. Tracing is exported
// only when OTEL_EXPORTER_OTLP_ENDPOINT is set: tests and a bare local run
// should not try to reach a collector that does not exist.
func Setup(ctx context.Context, o Options) (*Telemetry, error) {
	res := resource.NewSchemaless(
		attribute.String("service.name", o.Service),
		attribute.String("service.version", o.Version),
		attribute.String("deployment.environment", o.Env),
	)

	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); endpoint != "" {
		exp, err := otlptrace.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: otlp trace exporter: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(traceOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	meterExp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: prometheus exporter: %w", err)
	}
	mp := metric.NewMeterProvider(
		metric.WithReader(meterExp),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	return &Telemetry{
		Registry: reg,
		Metrics:  promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
		tracer:   tp.Tracer("echo/" + o.Service),
		shutdown: []func(context.Context) error{tp.Shutdown, mp.Shutdown},
	}, nil
}

// Tracer returns this service's tracer, for spans the HTTP layer does not own.
func (t *Telemetry) Tracer() trace.Tracer { return t.tracer }

// Shutdown flushes spans and metrics. It is called last, after every server
// that produces them has stopped.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, fn := range t.shutdown {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}
