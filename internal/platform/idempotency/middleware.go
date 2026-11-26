package idempotency

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	apperr "github.com/donaina/echo/internal/platform/errors"
)

// Backend is what the middleware needs from storage. The SQL Store is the
// production implementation; a test supplies its own.
type Backend interface {
	Begin(ctx context.Context, scope, key, fingerprint string, ttl time.Duration) (Record, bool, error)
	Complete(ctx context.Context, id string, status int, body []byte, resourceID *string) error
}

// Middleware handles Idempotency-Key on a money route.
type Middleware struct {
	backend Backend
	ttl     time.Duration
	log     *slog.Logger
}

// NewMiddleware returns middleware over backend. scope is the caller's
// identity plus the route; caller is read from X-Echo-Caller, which the
// service-token middleware requires.
func NewMiddleware(backend Backend, log *slog.Logger) *Middleware {
	if log == nil {
		log = slog.Default()
	}
	return &Middleware{backend: backend, ttl: TTL, log: log}
}

const maxBody = 1 << 20

// Handler wraps next. A request without a key is refused before it reaches
// the route; a replay returns the stored response without executing anything.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 255 {
			apperr.Write(w, r, apperr.New(apperr.IdempotencyKeyRequired,
				"a valid Idempotency-Key header is required"))
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			apperr.Write(w, r, apperr.New(apperr.ValidationFailed, "the request body could not be read"))
			return
		}
		if len(body) > maxBody {
			apperr.Write(w, r, apperr.New(apperr.ValidationFailed, "the request body is too large"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		scope := scopeOf(r)
		fingerprint := Fingerprint(r.Method, r.URL.Path, body)
		rec, inserted, err := m.backend.Begin(r.Context(), scope, key, fingerprint, m.ttl)
		if err != nil {
			m.log.ErrorContext(r.Context(), "idempotency: begin", "err", err)
			apperr.Write(w, r, apperr.New(apperr.InternalError, "internal error"))
			return
		}

		if !inserted {
			switch {
			case rec.Fingerprint != fingerprint:
				apperr.Write(w, r, apperr.New(apperr.IdempotencyKeyReused,
					"this key was used for a different request").
					WithMeta("original_request_fingerprint", rec.Fingerprint).
					WithMeta("received_request_fingerprint", fingerprint))
				return
			case rec.Status == "completed":
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(rec.ResponseStatus)
				if r.Method != http.MethodHead {
					_, _ = w.Write(rec.ResponseBody)
				}
				return
			default:
				apperr.Write(w, r, apperr.New(apperr.RequestInProgress,
					"the original request is still running; retry later"))
				return
			}
		}

		recorder := &responseRecorder{header: make(http.Header), status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		if err := m.backend.Complete(r.Context(), rec.ID, recorder.status, recorder.body.Bytes(), nil); err != nil {
			// The request has already executed. Losing the record costs a
			// replay and a later 409; deleting the key would invite a second
			// movement, which is worse.
			m.log.ErrorContext(r.Context(), "idempotency: complete", "err", err)
		}

		for k, vs := range recorder.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Idempotent-Replayed", "false")
		w.WriteHeader(recorder.status)
		if r.Method != http.MethodHead {
			_, _ = w.Write(recorder.body.Bytes())
		}
	})
}

func scopeOf(r *http.Request) string {
	caller := strings.TrimSpace(r.Header.Get("X-Echo-Caller"))
	return caller + "|" + r.Method + " " + r.URL.Path
}

type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}
