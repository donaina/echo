// Package servicetoken is the temporary service-to-service boundary that
// Stage 4 replaces with mTLS and a signed caller identity. It is a shared
// secret and a caller name, and nothing more.
package servicetoken

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	apperr "github.com/donaina/echo/internal/platform/errors"
)

const (
	// TokenHeader carries the shared secret.
	TokenHeader = "X-Echo-Service-Token"
	// CallerHeader names the calling service; it is recorded as created_by.
	CallerHeader = "X-Echo-Caller"
)

// Require refuses a request without the token and a caller name. The token is
// compared as a hash, so the comparison reveals neither its length nor a
// matching prefix.
func Require(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get(TokenHeader)))
		if token == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			apperr.Write(w, r, apperr.New(apperr.Unauthenticated,
				"a valid service token is required"))
			return
		}
		if Caller(r) == "" {
			apperr.Write(w, r, apperr.New(apperr.ValidationFailed,
				CallerHeader+" the caller identity is required").
				WithMeta("field", CallerHeader))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Caller is the authenticated service name.
func Caller(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(CallerHeader))
}

// Correlation is the request's correlation id: the gateway's header if there
// is one, otherwise the request id, otherwise a stable placeholder for a call
// that did not pass through the transport.
func Correlation(r *http.Request) string {
	if c := r.Header.Get("X-Correlation-Id"); c != "" {
		return c
	}
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	return "uncorrelated"
}
