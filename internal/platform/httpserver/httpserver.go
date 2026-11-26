// Package httpserver gives every service the same lifecycle: bounded timeouts,
// request and correlation ids, tracing, panic recovery, access logging,
// metrics, probes, a drain period and a graceful shutdown. A service supplies
// its routes; it does not re-solve shutdown.
package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	apperr "github.com/donaina/echo/internal/platform/errors"
	"github.com/donaina/echo/internal/platform/logging"
)

// Options configures a server. Zero timeouts take the defaults in New; a zero
// DrainDelay means no wait, which is what tests want.
type Options struct {
	Service     string
	Addr        string
	MetricsAddr string
	Handler     http.Handler
	Logger      *slog.Logger
	Probes      *Probes
	Metrics     http.Handler
	Registry    prometheus.Registerer

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	DrainDelay        time.Duration
	ShutdownTimeout   time.Duration
}

// Server is an HTTP server with the platform's lifecycle.
type Server struct {
	opts    Options
	probes  *Probes
	log     *slog.Logger
	handler http.Handler
	http    *http.Server
	metrics *http.Server
}

// New builds the server and its middleware. It does not listen.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Probes == nil {
		o.Probes = NewProbes(o.Logger)
	}
	if o.ReadHeaderTimeout <= 0 {
		o.ReadHeaderTimeout = 5 * time.Second
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = 10 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 15 * time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 60 * time.Second
	}
	if o.MaxHeaderBytes <= 0 {
		o.MaxHeaderBytes = 16 << 10
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 10 * time.Second
	}
	if o.DrainDelay < 0 {
		o.DrainDelay = 0
	}

	mux := http.NewServeMux()
	o.Probes.Register(mux)
	if o.Handler != nil {
		mux.Handle("/", o.Handler)
	}

	s := &Server{opts: o, probes: o.Probes, log: o.Logger}
	s.handler = s.instrument(mux, newMetrics(o.Service, o.Registry))
	s.http = &http.Server{
		Addr:              o.Addr,
		Handler:           s.handler,
		ReadHeaderTimeout: o.ReadHeaderTimeout,
		ReadTimeout:       o.ReadTimeout,
		WriteTimeout:      o.WriteTimeout,
		IdleTimeout:       o.IdleTimeout,
		MaxHeaderBytes:    o.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(o.Logger.Handler(), slog.LevelWarn),
	}
	if o.Metrics != nil && o.MetricsAddr != "" {
		mm := http.NewServeMux()
		mm.Handle("GET /metrics", o.Metrics)
		s.metrics = &http.Server{
			Addr:              o.MetricsAddr,
			Handler:           mm,
			ReadHeaderTimeout: 5 * time.Second,
		}
	}
	return s
}

// Handler returns the fully wrapped handler, for tests that do not need a
// socket.
func (s *Server) Handler() http.Handler { return s.handler }

// Run listens on the configured addresses, serving until ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.opts.Addr, err)
	}
	var mln net.Listener
	if s.metrics != nil {
		mln, err = net.Listen("tcp", s.opts.MetricsAddr)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("listen metrics %s: %w", s.opts.MetricsAddr, err)
		}
	}
	return s.serve(ctx, ln, mln)
}

// Serve runs on an existing listener, which is how tests pick a free port.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return s.serve(ctx, ln, nil)
}

func (s *Server) serve(ctx context.Context, ln, mln net.Listener) error {
	errCh := make(chan error, 2)
	go func() {
		err := s.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	if mln != nil {
		go func() {
			err := s.metrics.Serve(mln)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			errCh <- err
		}()
	}
	s.log.Info("listening", "addr", ln.Addr().String(), "metrics_addr", s.opts.MetricsAddr)

	select {
	case <-ctx.Done():
		// Readiness fails first, so load balancers stop sending new requests
		// while the requests already in flight are allowed to finish.
		s.probes.Drain(s.opts.DrainDelay)
		shutCtx, cancel := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
		defer cancel()
		var errs []error
		errs = append(errs, s.http.Shutdown(shutCtx))
		if s.metrics != nil {
			errs = append(errs, s.metrics.Shutdown(shutCtx))
		}
		return errors.Join(errs...)
	case err := <-errCh:
		return err
	}
}

// instrument is the whole request pipeline in one place, so the order is
// visible: identify, trace, log, recover, measure.
func (s *Server) instrument(next *http.ServeMux, m *metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id := sanitizeID(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = newID()
		}
		corr := sanitizeID(r.Header.Get("X-Correlation-Id"))
		if corr == "" {
			corr = id
		}
		r.Header.Set("X-Request-Id", id)
		r.Header.Set("X-Correlation-Id", corr)
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Correlation-Id", corr)

		route := routePattern(next, r)
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("echo/"+s.opts.Service).Start(ctx, r.Method+" "+route,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
				attribute.String("http.route", route),
			))
		defer span.End()

		log := s.log.With("request_id", id, "correlation_id", corr,
			"method", r.Method, "route", route)
		ctx = logging.WithLogger(ctx, log)
		r = r.WithContext(ctx)

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				log.ErrorContext(ctx, "panic serving request", "panic", v, "stack", string(debug.Stack()))
				span.SetStatus(codes.Error, "panic")
				if !rec.wrote {
					apperr.Write(rec, r, apperr.New(apperr.InternalError, "internal error"))
				}
			}
			d := time.Since(start)
			if rec.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
			span.SetAttributes(attribute.Int("http.response.status_code", rec.status))
			m.observe(route, r.Method, rec.status, d)
			log.InfoContext(ctx, "request", "status", rec.status,
				"duration_ms", d.Milliseconds(), "bytes", rec.bytes)
		}()

		next.ServeHTTP(rec, r)
	})
}

// routePattern returns the matched pattern, not the raw path: a metric label
// must not grow a new value for every account id that passes through.
func routePattern(mux *http.ServeMux, r *http.Request) string {
	_, pattern := mux.Handler(r)
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}

// recorder remembers the status and size so the log line can report them.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (r *recorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.status = code
	r.wrote = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(service string, reg prometheus.Registerer) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "echo_http_requests_total",
			Help:        "HTTP requests, by route, method and status.",
			ConstLabels: prometheus.Labels{"service": service},
		}, []string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "echo_http_request_duration_seconds",
			Help:        "HTTP request duration, by route.",
			ConstLabels: prometheus.Labels{"service": service},
			Buckets:     prometheus.DefBuckets,
		}, []string{"route"}),
	}
	if reg != nil {
		reg.MustRegister(m.requests, m.duration)
	}
	return m
}

func (m *metrics) observe(route, method string, status int, d time.Duration) {
	m.requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route).Observe(d.Seconds())
}

// newID returns a random 128-bit identifier, hex encoded.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// sanitizeID keeps client-supplied ids to a shape that is safe to log.
func sanitizeID(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Probes answers /healthz and /readyz.
type Probes struct {
	mu     sync.RWMutex
	ready  bool
	checks []namedCheck
	log    *slog.Logger
}

type namedCheck struct {
	name string
	fn   func(context.Context) error
}

// NewProbes returns probes that are not ready until Ready is called.
func NewProbes(log *slog.Logger) *Probes {
	if log == nil {
		log = slog.Default()
	}
	return &Probes{log: log}
}

// Add registers a dependency check that /readyz runs.
func (p *Probes) Add(name string, fn func(context.Context) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks = append(p.checks, namedCheck{name: name, fn: fn})
}

// Ready reports that the service can serve traffic.
func (p *Probes) Ready() {
	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()
}

// Drain makes readiness fail immediately, then waits for load balancers to
// notice before the listener closes.
func (p *Probes) Drain(d time.Duration) {
	p.mu.Lock()
	p.ready = false
	p.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

// Register mounts the probes on mux.
func (p *Probes) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", p.health)
	mux.HandleFunc("GET /readyz", p.readiness)
}

func (p *Probes) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (p *Probes) readiness(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	ready := p.ready
	checks := append([]namedCheck(nil), p.checks...)
	p.mu.RUnlock()

	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
		return
	}
	var failed []string
	for _, c := range checks {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := c.fn(ctx)
		cancel()
		if err != nil {
			p.log.WarnContext(r.Context(), "readiness check failed", "check", c.name, "err", err)
			failed = append(failed, c.name)
		}
	}
	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not_ready",
			"failed": failed,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
