package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// Service is the configuration every service shares. The gateway passes an
// empty dbEnv because it holds no money state and therefore no database.
type Service struct {
	Name            string
	Env             string
	HTTPAddr        string
	MetricsAddr     string
	DatabaseURL     string
	LogLevel        slog.Level
	LogFormat       string
	MaxConns        int
	DrainDelay      time.Duration
	ShutdownTimeout time.Duration
}

// LoadService reads the common configuration and validates it. dbEnv names the
// service's database URL variable, or is "" for a service without a database.
// The database URL deliberately has no default: a value that is wrong in
// production should never be guessed at startup.
func LoadService(r *Reader, name, httpAddr, metricsAddr, dbEnv string) (Service, error) {
	prefix := strings.ToUpper(name)
	s := Service{
		Name:            name,
		Env:             r.String("ENV", "local"),
		HTTPAddr:        r.String(prefix+"_HTTP_ADDR", httpAddr),
		MetricsAddr:     r.String(prefix+"_METRICS_ADDR", metricsAddr),
		LogLevel:        r.Level("LOG_LEVEL", slog.LevelInfo),
		LogFormat:       strings.ToLower(r.String("LOG_FORMAT", "json")),
		MaxConns:        r.Int(prefix+"_DB_MAX_CONNS", 10),
		DrainDelay:      r.Duration("DRAIN_DELAY", 3*time.Second),
		ShutdownTimeout: r.Duration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}
	if dbEnv != "" {
		s.DatabaseURL = r.Required(dbEnv)
		if s.DatabaseURL != "" {
			if err := checkDatabaseURL(dbEnv, s.DatabaseURL); err != nil {
				r.Fail("%v", err)
			}
		}
	}
	if err := r.Err(); err != nil {
		return Service{}, err
	}
	if err := s.Validate(); err != nil {
		return Service{}, err
	}
	return s, nil
}

// Validate checks the fields that do not depend on a Reader.
func (s Service) Validate() error {
	prefix := strings.ToUpper(s.Name)
	if s.HTTPAddr == "" {
		return fmt.Errorf("configuration: %s_HTTP_ADDR must not be empty", prefix)
	}
	if s.MetricsAddr == "" {
		return fmt.Errorf("configuration: %s_METRICS_ADDR must not be empty", prefix)
	}
	if s.LogFormat != "json" && s.LogFormat != "text" {
		return fmt.Errorf("configuration: LOG_FORMAT must be json or text, got %q", s.LogFormat)
	}
	if s.MaxConns < 1 {
		return fmt.Errorf("configuration: %s_DB_MAX_CONNS must be at least 1", prefix)
	}
	if s.ShutdownTimeout <= 0 {
		return fmt.Errorf("configuration: SHUTDOWN_TIMEOUT must be positive")
	}
	if s.DrainDelay < 0 {
		return fmt.Errorf("configuration: DRAIN_DELAY must not be negative")
	}
	return nil
}

func checkDatabaseURL(key, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a postgres:// URL: %v", key, err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("%s must be a postgres:// URL, got scheme %q", key, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s must name a host", key)
	}
	return nil
}
