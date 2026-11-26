package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	apperr "github.com/donaina/echo/internal/platform/errors"
	"github.com/donaina/echo/internal/platform/idempotency"
	"github.com/donaina/echo/internal/platform/servicetoken"
)

// Tenant is the single tenant this release serves (ADR 0007).
const Tenant = "t_main"

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Service validates a transfer request and opens its saga.
type Service struct {
	store *Store
	wake  func()
}

// NewService returns a Service. wake is called after a saga is created so the
// worker starts on it without waiting for the poll.
func NewService(store *Store, wake func()) *Service {
	return &Service{store: store, wake: wake}
}

// CreateCommand is one transfer instruction.
type CreateCommand struct {
	FromAccount string
	Destination string
	AmountMinor int64
	Currency    string
	Reference   string
}

// Create opens a saga and returns it in the received state.
func (s *Service) Create(ctx context.Context, cmd CreateCommand, by, correlationID string) (Saga, error) {
	from, err := requireID("from_account", cmd.FromAccount)
	if err != nil {
		return Saga{}, err
	}
	destination := strings.TrimSpace(cmd.Destination)
	if destination == "" {
		return Saga{}, validation("destination", "must not be empty")
	}
	if cmd.AmountMinor <= 0 {
		return Saga{}, validation("amount_minor", "must be a positive number of minor units")
	}
	if !currencyCode.MatchString(cmd.Currency) {
		return Saga{}, validation("currency", "must be a three-letter ISO 4217 code")
	}
	if strings.TrimSpace(by) == "" {
		return Saga{}, validation("X-Echo-Caller", "the caller identity is required")
	}

	saga, err := s.store.Create(ctx, ulid.Make().String(), Tenant, by, correlationID, Request{
		FromAccount: from,
		Destination: destination,
		AmountMinor: cmd.AmountMinor,
		Currency:    cmd.Currency,
		Reference:   strings.TrimSpace(cmd.Reference),
	})
	if err != nil {
		return Saga{}, err
	}
	if s.wake != nil {
		s.wake()
	}
	return saga, nil
}

// Get returns one saga.
func (s *Service) Get(ctx context.Context, id string) (Saga, error) {
	id, err := requireID("transfer_id", id)
	if err != nil {
		return Saga{}, err
	}
	return s.store.Get(ctx, id)
}

// NewHandler returns the transfer service's internal HTTP face.
func NewHandler(svc *Service, serviceToken string, idem *idempotency.Middleware) *http.ServeMux {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return servicetoken.Require(serviceToken, next)
	}
	money := func(next http.Handler) http.Handler {
		if idem != nil {
			next = idem.Handler(next)
		}
		return guard(next)
	}
	mux.Handle("POST /transfers", money(http.HandlerFunc(h.create)))
	mux.Handle("GET /transfers/{id}", guard(http.HandlerFunc(h.get)))
	return mux
}

type handler struct {
	svc *Service
}

type createRequest struct {
	FromAccount string `json:"from_account"`
	Destination string `json:"destination"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Reference   string `json:"reference"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	req, err := decode[createRequest](w, r)
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	saga, err := h.svc.Create(r.Context(), CreateCommand(req), servicetoken.Caller(r), servicetoken.Correlation(r))
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sagaBody(saga, true))
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	saga, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sagaBody(saga, false))
}

type sagaResponse struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	FromAccount   string            `json:"from_account,omitempty"`
	Destination   string            `json:"destination,omitempty"`
	AmountMinor   int64             `json:"amount_minor,omitempty"`
	Currency      string            `json:"currency,omitempty"`
	Reference     string            `json:"reference,omitempty"`
	FailureReason *string           `json:"failure_reason,omitempty"`
	RemoteRef     *string           `json:"remote_ref,omitempty"`
	CreatedAt     time.Time         `json:"created_at,omitempty"`
	Links         map[string]string `json:"links,omitempty"`
}

func sagaBody(s Saga, links bool) sagaResponse {
	out := sagaResponse{
		ID:            s.ID,
		Status:        s.State,
		FromAccount:   s.Request.FromAccount,
		Destination:   s.Request.Destination,
		AmountMinor:   s.Request.AmountMinor,
		Currency:      s.Request.Currency,
		Reference:     s.Request.Reference,
		FailureReason: s.FailureReason,
		RemoteRef:     s.RemoteRef,
		CreatedAt:     s.CreatedAt,
	}
	if links {
		out.Links = map[string]string{"self": "/transfers/" + s.ID}
	}
	return out
}

func requireID(field, raw string) (string, error) {
	id, err := ulid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", validation(field, "must be a ULID")
	}
	return id.String(), nil
}

func validation(field, message string) error {
	return apperr.New(apperr.ValidationFailed, fmt.Sprintf("%s %s", field, message)).
		WithMeta("field", field)
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, apperr.New(apperr.ValidationFailed,
			"the request body is not valid JSON for this endpoint")
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
