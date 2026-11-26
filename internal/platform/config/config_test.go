package config

import (
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) Lookup {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestReaderNamesTheInvalidVariable(t *testing.T) {
	r := New(lookup(map[string]string{"WALLET_DB_MAX_CONNS": "many"}))
	r.Int("WALLET_DB_MAX_CONNS", 10)

	err := r.Err()
	if err == nil {
		t.Fatal("expected an error for a non-integer value")
	}
	if !strings.Contains(err.Error(), "WALLET_DB_MAX_CONNS") {
		t.Fatalf("error does not name the variable: %v", err)
	}
}

func TestReaderRequiresKey(t *testing.T) {
	r := New(lookup(nil))
	if got := r.Required("WALLET_DATABASE_URL"); got != "" {
		t.Fatalf("Required returned %q, want empty", got)
	}
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "WALLET_DATABASE_URL is required") {
		t.Fatalf("error = %v, want a required-variable error", err)
	}
}

func TestReaderKeepsReadingAfterAProblem(t *testing.T) {
	r := New(lookup(map[string]string{
		"LOG_LEVEL": "loud",
		"ENV":       "test",
	}))
	r.Level("LOG_LEVEL", 0)
	if got := r.String("ENV", "local"); got != "test" {
		t.Fatalf("reading stopped after a problem: ENV = %q", got)
	}
	if err := r.Err(); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("error = %v, want it to name LOG_LEVEL", err)
	}
}

func TestReaderParsesValues(t *testing.T) {
	r := New(lookup(map[string]string{
		"MAX":       "25",
		"WAIT":      "1500ms",
		"ENABLED":   "true",
		"LOG_LEVEL": "warn",
	}))
	if got := r.Int("MAX", 1); got != 25 {
		t.Errorf("Int = %d, want 25", got)
	}
	if got := r.Duration("WAIT", time.Second); got != 1500*time.Millisecond {
		t.Errorf("Duration = %v, want 1500ms", got)
	}
	if got := r.Bool("ENABLED", false); !got {
		t.Error("Bool = false, want true")
	}
	if got := r.Level("LOG_LEVEL", 0); got.String() != "WARN" {
		t.Errorf("Level = %v, want WARN", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
}

func TestLoadServiceRejectsBadDatabaseURL(t *testing.T) {
	r := New(lookup(map[string]string{
		"WALLET_DATABASE_URL": "mysql://nope",
	}))
	_, err := LoadService(r, "wallet", ":8082", ":9082", "WALLET_DATABASE_URL")
	if err == nil || !strings.Contains(err.Error(), "WALLET_DATABASE_URL") {
		t.Fatalf("error = %v, want a database URL error naming the variable", err)
	}
}

func TestLoadServiceDefaults(t *testing.T) {
	r := New(lookup(map[string]string{
		"AUTH_DATABASE_URL": "postgres://user:pass@localhost:5432/auth?sslmode=disable",
	}))
	cfg, err := LoadService(r, "auth", ":8081", ":9081", "AUTH_DATABASE_URL")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if cfg.HTTPAddr != ":8081" || cfg.MetricsAddr != ":9081" {
		t.Errorf("addresses = %q / %q", cfg.HTTPAddr, cfg.MetricsAddr)
	}
	if cfg.Env != "local" || cfg.LogFormat != "json" || cfg.MaxConns != 10 {
		t.Errorf("defaults = %+v", cfg)
	}
}
