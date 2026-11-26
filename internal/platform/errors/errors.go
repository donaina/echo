// Package errors defines the typed errors every service returns. A money path
// returns one of these, never a formatted string: the code is the contract the
// caller keys on, and the kind is what tells the caller whether a retry is
// safe. See docs/reference/errors.md.
package errors

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strings"
)

// Kind says what the caller may do with an error.
type Kind int

const (
	// Rejection means the request was understood and refused. Nothing moved.
	Rejection Kind = iota
	// Failure means something broke. Retrying with the same key may work.
	Failure
	// Unknown means money may have moved. The caller must query, not retry.
	Unknown
)

func (k Kind) String() string {
	switch k {
	case Rejection:
		return "rejection"
	case Failure:
		return "failure"
	case Unknown:
		return "unknown"
	}
	return "unknown"
}

// Code is a stable identifier. Clients key on it; it never changes meaning.
type Code string

const (
	ValidationFailed       Code = "validation_failed"
	InvalidCursor          Code = "invalid_cursor"
	IdempotencyKeyRequired Code = "idempotency_key_required"
	Unauthenticated        Code = "unauthenticated"
	TokenExpired           Code = "token_expired"
	Forbidden              Code = "forbidden"
	AccountNotFound        Code = "account_not_found"
	TransferNotFound       Code = "transfer_not_found"
	MethodNotAllowed       Code = "method_not_allowed"
	Conflict               Code = "conflict"
	RequestInProgress      Code = "request_in_progress"
	InsufficientFunds      Code = "insufficient_funds"
	AccountFrozen          Code = "account_frozen"
	AccountClosed          Code = "account_closed"
	CurrencyMismatch       Code = "currency_mismatch"
	SelfTransfer           Code = "self_transfer"
	IdempotencyKeyReused   Code = "idempotency_key_reused"
	AmountOutOfRange       Code = "amount_out_of_range"
	RateLimited            Code = "rate_limited"
	InternalError          Code = "internal_error"
	DependencyUnavailable  Code = "dependency_unavailable"
	OutcomeUnknown         Code = "outcome_unknown"

	// Operator-facing codes. These never appear on a customer endpoint.
	CommandScopeDenied     Code = "command_scope_denied"
	CommandReasonRequired  Code = "command_reason_required"
	PostingRejected        Code = "posting_rejected"
	BreakNotFound          Code = "break_not_found"
	BreakInvalidTransition Code = "break_invalid_transition"
	SettlementCycleOpen    Code = "settlement_cycle_open"
)

type spec struct {
	status int
	kind   Kind
	title  string
}

var specs = map[Code]spec{
	ValidationFailed:       {http.StatusBadRequest, Rejection, "Validation failed"},
	InvalidCursor:          {http.StatusBadRequest, Rejection, "Invalid cursor"},
	IdempotencyKeyRequired: {http.StatusBadRequest, Rejection, "Idempotency key required"},
	Unauthenticated:        {http.StatusUnauthorized, Rejection, "Unauthenticated"},
	TokenExpired:           {http.StatusUnauthorized, Rejection, "Token expired"},
	Forbidden:              {http.StatusForbidden, Rejection, "Forbidden"},
	AccountNotFound:        {http.StatusNotFound, Rejection, "Account not found"},
	TransferNotFound:       {http.StatusNotFound, Rejection, "Transfer not found"},
	MethodNotAllowed:       {http.StatusMethodNotAllowed, Rejection, "Method not allowed"},
	Conflict:               {http.StatusConflict, Rejection, "Conflict"},
	RequestInProgress:      {http.StatusConflict, Rejection, "Request in progress"},
	InsufficientFunds:      {http.StatusUnprocessableEntity, Rejection, "Insufficient funds"},
	AccountFrozen:          {http.StatusUnprocessableEntity, Rejection, "Account frozen"},
	AccountClosed:          {http.StatusUnprocessableEntity, Rejection, "Account closed"},
	CurrencyMismatch:       {http.StatusUnprocessableEntity, Rejection, "Currency mismatch"},
	SelfTransfer:           {http.StatusUnprocessableEntity, Rejection, "Self transfer"},
	IdempotencyKeyReused:   {http.StatusUnprocessableEntity, Rejection, "Idempotency key reused"},
	AmountOutOfRange:       {http.StatusUnprocessableEntity, Rejection, "Amount out of range"},
	RateLimited:            {http.StatusTooManyRequests, Rejection, "Rate limited"},
	InternalError:          {http.StatusInternalServerError, Failure, "Internal error"},
	DependencyUnavailable:  {http.StatusServiceUnavailable, Failure, "Dependency unavailable"},
	OutcomeUnknown:         {http.StatusServiceUnavailable, Unknown, "Outcome unknown"},

	CommandScopeDenied:     {http.StatusForbidden, Rejection, "Command not permitted"},
	CommandReasonRequired:  {http.StatusBadRequest, Rejection, "Command reason required"},
	PostingRejected:        {http.StatusConflict, Rejection, "Posting rejected"},
	BreakNotFound:          {http.StatusNotFound, Rejection, "Break not found"},
	BreakInvalidTransition: {http.StatusConflict, Rejection, "Break transition not permitted"},
	SettlementCycleOpen:    {http.StatusConflict, Rejection, "Settlement cycle still open"},
}

// Status is the HTTP status this code maps to.
func (c Code) Status() int { return specs[c].status }

// Kind says whether a retry is safe.
func (c Code) Kind() Kind { return specs[c].kind }

// Title is the short human summary in a problem document.
func (c Code) Title() string { return specs[c].title }

// Valid reports whether the code is part of the contract.
func (c Code) Valid() bool {
	_, ok := specs[c]
	return ok
}

// Error is a typed domain error.
type Error struct {
	Code    Code
	Message string
	Meta    map[string]any
	cause   error
}

// New returns an error with a safe, developer-facing message.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap returns an error that keeps cause for logs and unwrapping.
func Wrap(code Code, cause error, message string) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.cause.Error()
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.cause }

// Kind says whether a retry is safe.
func (e *Error) Kind() Kind { return e.Code.Kind() }

// Status is the HTTP status this error maps to.
func (e *Error) Status() int { return e.Code.Status() }

// WithMeta returns a copy carrying structured context. Meta never contains
// PII, and it is dropped from 5xx responses.
func (e *Error) WithMeta(key string, value any) *Error {
	c := *e
	c.Meta = make(map[string]any, len(e.Meta)+1)
	for k, v := range e.Meta {
		c.Meta[k] = v
	}
	c.Meta[key] = value
	return &c
}

// From maps any error to the typed model. A deadline on a money call is
// Unknown, never a failure: the caller must query, not retry (Article VII).
// Anything unrecognised becomes internal_error with no detail in its message.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if stderrors.As(err, &e) {
		return e
	}
	switch {
	case stderrors.Is(err, context.DeadlineExceeded):
		return Wrap(OutcomeUnknown, err, "the outcome is not known; query the resource")
	case stderrors.Is(err, context.Canceled):
		return Wrap(InternalError, err, "the request was canceled")
	default:
		return Wrap(InternalError, err, "internal error")
	}
}

// problem is RFC 9457 problem+json.
type problem struct {
	Type      string         `json:"type"`
	Title     string         `json:"title"`
	Status    int            `json:"status"`
	Code      Code           `json:"code"`
	Detail    string         `json:"detail,omitempty"`
	Instance  string         `json:"instance,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// Write renders err as problem+json. A 5xx never carries Meta: internal state
// in an error response is a leak, not an explanation.
func Write(w http.ResponseWriter, r *http.Request, err error) {
	e := From(err)
	if e == nil {
		e = New(InternalError, "internal error")
	}
	status := e.Status()
	meta := e.Meta
	if status >= 500 {
		meta = nil
	}
	p := problem{
		Type:      "https://errors.echo.dev/" + strings.ReplaceAll(string(e.Code), "_", "-"),
		Title:     e.Code.Title(),
		Status:    status,
		Code:      e.Code,
		Detail:    e.Message,
		Instance:  r.URL.Path,
		RequestID: r.Header.Get("X-Request-Id"),
		Meta:      meta,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(p)
}
