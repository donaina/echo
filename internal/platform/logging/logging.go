// Package logging builds the one structured logger a service uses. Every line
// carries the service, its version and the environment, so a shared log stream
// can be filtered without guessing which process wrote a line.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// Options configures a service logger.
type Options struct {
	Service string
	Version string
	Env     string
	Level   slog.Level
	Format  string // "json" or "text"
}

// New returns a logger that stamps every line with the service's identity.
func New(w io.Writer, o Options) *slog.Logger {
	ho := &slog.HandlerOptions{Level: o.Level}
	var h slog.Handler
	if strings.EqualFold(o.Format, "text") {
		h = slog.NewTextHandler(w, ho)
	} else {
		h = slog.NewJSONHandler(w, ho)
	}
	return slog.New(h).With(
		slog.String("service", o.Service),
		slog.String("version", o.Version),
		slog.String("env", o.Env),
	)
}

type ctxKey struct{}

// WithLogger puts l in ctx, so code below the transport logs with the request's
// fields without every function taking a logger argument.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the logger carried by ctx, or fallback when there is none.
func From(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return fallback
}

// Redact replaces a sensitive value. Logs never carry an amount, an account
// number or a token; use this where the field must exist for correlation but
// its value must not be readable.
func Redact(string) string { return "[redacted]" }
