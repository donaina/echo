// Package config reads configuration from the environment once, at startup.
// No service reads os.Getenv directly; values are parsed into a typed struct
// whose validation fails the process with a message naming the key.
package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Lookup is the shape of os.LookupEnv, so tests can supply their own.
type Lookup func(string) (string, bool)

// Reader collects problems instead of returning an error on every read, so a
// single startup reports every bad variable rather than only the first.
type Reader struct {
	lookup   Lookup
	problems []string
}

// New returns a Reader over lookup.
func New(lookup Lookup) *Reader {
	return &Reader{lookup: lookup}
}

// String returns the variable's value, or def when it is absent or blank.
func (r *Reader) String(key, def string) string {
	v, ok := r.lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// Required returns the variable's value, or records a problem and returns "".
func (r *Reader) Required(key string) string {
	v, ok := r.lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		r.Fail("%s is required", key)
		return ""
	}
	return strings.TrimSpace(v)
}

// Int parses an integer variable. An unparseable value is a problem, not a
// silent fallback to the default.
func (r *Reader) Int(key string, def int) int {
	raw := r.String(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		r.Fail("%s must be an integer, got %q", key, raw)
		return def
	}
	return n
}

// Bool parses a boolean variable.
func (r *Reader) Bool(key string, def bool) bool {
	raw := r.String(key, "")
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		r.Fail("%s must be true or false, got %q", key, raw)
		return def
	}
	return b
}

// Duration parses a Go duration such as "5s" or "1m30s".
func (r *Reader) Duration(key string, def time.Duration) time.Duration {
	raw := r.String(key, "")
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		r.Fail("%s must be a duration like 5s, got %q", key, raw)
		return def
	}
	if d < 0 {
		r.Fail("%s must not be negative", key)
		return def
	}
	return d
}

// Level parses a log level name.
func (r *Reader) Level(key string, def slog.Level) slog.Level {
	raw := r.String(key, "")
	if raw == "" {
		return def
	}
	l, err := ParseLevel(raw)
	if err != nil {
		r.Fail("%s %v", key, err)
		return def
	}
	return l
}

// ParseLevel maps a level name to a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("must be one of debug, info, warn, error, got %q", s)
}

// Fail records a configuration problem. Reading continues so that startup can
// report every bad variable at once.
func (r *Reader) Fail(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

// Err returns every problem found, or nil.
func (r *Reader) Err() error {
	if len(r.problems) == 0 {
		return nil
	}
	return fmt.Errorf("configuration: %s", strings.Join(r.problems, "; "))
}
