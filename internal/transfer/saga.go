// Package transfer orchestrates a payment across the platform and a remote
// destination. It owns the saga's state and no balance: every movement in the
// ledger belongs to the wallet, and every call to the destination is
// idempotent by the saga id.
package transfer

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

// The states a saga moves through. Only completed and failed are terminal.
const (
	StateReceived     = "received"
	StateReserved     = "reserved"
	StateDebited      = "debited"
	StateCompleted    = "completed"
	StateFailed       = "failed"
	StateCompensating = "compensating"
	StateResolving    = "resolving"
)

// Request is the original instruction, stored so a resume never depends on
// the caller still being there.
type Request struct {
	FromAccount string `json:"from_account"`
	Destination string `json:"destination"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Reference   string `json:"reference"`
}

// Saga is one transfer and where it is.
type Saga struct {
	ID            string
	TenantID      string
	State         string
	Request       Request
	CreatedBy     string
	CorrelationID string
	HoldID        *string
	CaptureTxID   *string
	Attempts      int32
	NextAttemptAt time.Time
	LastError     *string
	RemoteRef     *string
	FailureReason *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreditStatus is the only honest answer a remote destination gives: it
// applied, it refused, or it did not say.
type CreditStatus int

const (
	Applied CreditStatus = iota
	Rejected
	Unknown
)

func (s CreditStatus) String() string {
	switch s {
	case Applied:
		return "applied"
	case Rejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// CreditRequest is one remote credit.
type CreditRequest struct {
	Reference   string
	Source      string
	Destination string
	AmountMinor int64
	Currency    string
}

// CreditResult is what the destination said, and what it calls the transfer.
type CreditResult struct {
	Status    CreditStatus
	RemoteRef string
	Code      string
	Detail    string
}

// Destination is the remote side of a transfer. Both methods must be safe to
// call more than once with the same reference.
type Destination interface {
	Credit(ctx context.Context, req CreditRequest) (CreditResult, error)
	Enquiry(ctx context.Context, reference string) (CreditResult, error)
}

// Wallet is the part of the wallet service the saga needs. Every method is
// idempotent, which is what makes a retry safe.
type Wallet interface {
	Hold(ctx context.Context, holdID, accountID string, amount int64, currency, reference string, ttl time.Duration) error
	Capture(ctx context.Context, holdID string) (transactionID string, err error)
	Release(ctx context.Context, holdID string) error
	Reverse(ctx context.Context, transactionID, reason string) error
}

// Rejection is a definitive refusal from the wallet or the destination. It
// changes the saga's path; it is never retried.
type Rejection struct {
	Code   string
	Detail string
}

func (r *Rejection) Error() string {
	if r.Detail == "" {
		return r.Code
	}
	return r.Code + ": " + r.Detail
}

func asRejection(err error) (*Rejection, bool) {
	var r *Rejection
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// Event is the terminal fact a saga emits, written to the outbox in the same
// transaction as the state change.
type Event struct {
	Type string
	Data map[string]any
}

// Orchestrator drives due sagas one step at a time. Any number can run: the
// claim and the conditional transitions keep them out of each other's way.
type Orchestrator struct {
	store       *Store
	wallet      Wallet
	destination Destination
	logger      *slog.Logger

	BatchSize int
	Lease     time.Duration
	Poll      time.Duration
	Backoff   time.Duration
	HoldTTL   time.Duration

	wake chan struct{}
}

// NewOrchestrator returns an Orchestrator with sensible defaults.
func NewOrchestrator(store *Store, wallet Wallet, destination Destination, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		store:       store,
		wallet:      wallet,
		destination: destination,
		logger:      logger,
		BatchSize:   10,
		Lease:       30 * time.Second,
		Poll:        time.Second,
		Backoff:     time.Second,
		HoldTTL:     15 * time.Minute,
		wake:        make(chan struct{}, 1),
	}
}

// Wake tells Run there is new work without waiting for the poll.
func (o *Orchestrator) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Run claims and steps due sagas until ctx is canceled.
func (o *Orchestrator) Run(ctx context.Context) {
	o.logger.Info("orchestrator started", "lease", o.Lease)
	for ctx.Err() == nil {
		n, err := o.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			o.logger.Warn("claim sagas", "err", err)
		}
		if n > 0 {
			continue
		}
		select {
		case <-ctx.Done():
		case <-o.wake:
		case <-time.After(o.Poll):
		}
	}
}

// RunOnce claims one batch and takes one step of each saga in it.
func (o *Orchestrator) RunOnce(ctx context.Context) (int, error) {
	batch, err := o.store.Claim(ctx, int32(o.BatchSize), o.Lease)
	if err != nil {
		return 0, err
	}
	for _, s := range batch {
		// A step that has started finishes, even during shutdown.
		o.step(context.WithoutCancel(ctx), s)
	}
	return len(batch), nil
}

func (o *Orchestrator) step(ctx context.Context, s Saga) {
	log := o.logger.With("transfer", s.ID, "state", s.State)
	switch s.State {
	case StateReceived:
		o.reserve(ctx, log, s)
	case StateReserved:
		o.debit(ctx, log, s)
	case StateDebited:
		o.credit(ctx, log, s)
	case StateCompensating:
		o.compensate(ctx, log, s)
	case StateResolving:
		o.resolve(ctx, log, s)
	}
}

func (o *Orchestrator) reserve(ctx context.Context, log *slog.Logger, s Saga) {
	err := o.wallet.Hold(ctx, s.ID, s.Request.FromAccount, s.Request.AmountMinor,
		s.Request.Currency, s.Request.Reference, o.HoldTTL)
	if err != nil {
		if rej, ok := asRejection(err); ok {
			o.fail(ctx, log, s, StateReceived, rej.Code)
			return
		}
		o.retry(ctx, log, s, err)
		return
	}
	o.advance(ctx, log, s, StateReceived, StateReserved, nil, nil)
}

func (o *Orchestrator) debit(ctx context.Context, log *slog.Logger, s Saga) {
	txID, err := o.wallet.Capture(ctx, s.ID)
	if err != nil {
		if rej, ok := asRejection(err); ok {
			_ = o.wallet.Release(ctx, s.ID)
			o.fail(ctx, log, s, StateReserved, rej.Code)
			return
		}
		o.retry(ctx, log, s, err)
		return
	}
	o.advance(ctx, log, s, StateReserved, StateDebited, nil, &txID)
}

func (o *Orchestrator) credit(ctx context.Context, log *slog.Logger, s Saga) {
	result, err := o.destination.Credit(ctx, CreditRequest{
		Reference:   s.ID,
		Source:      s.Request.FromAccount,
		Destination: s.Request.Destination,
		AmountMinor: s.Request.AmountMinor,
		Currency:    s.Request.Currency,
	})
	if err != nil {
		// A transport error is Unknown, never Rejected (Article VII).
		log.WarnContext(ctx, "destination outcome unknown", "err", err)
		o.advance(ctx, log, s, StateDebited, StateResolving, nil, nil)
		return
	}
	switch result.Status {
	case Applied:
		o.complete(ctx, log, s, StateDebited, result.RemoteRef)
	case Rejected:
		o.advance(ctx, log, s, StateDebited, StateCompensating, nil, nil)
	default:
		o.advance(ctx, log, s, StateDebited, StateResolving, nil, nil)
	}
}

func (o *Orchestrator) compensate(ctx context.Context, log *slog.Logger, s Saga) {
	if s.CaptureTxID == nil {
		o.fail(ctx, log, s, StateCompensating, "no capture transaction to reverse")
		return
	}
	err := o.wallet.Reverse(ctx, *s.CaptureTxID, "destination rejected")
	if err != nil {
		if rej, ok := asRejection(err); ok {
			log.ErrorContext(ctx, "compensation failed", "code", rej.Code)
			o.fail(ctx, log, s, StateCompensating, "compensation_failed: "+rej.Code)
			return
		}
		o.retry(ctx, log, s, err)
		return
	}
	o.fail(ctx, log, s, StateCompensating, "destination_rejected")
}

func (o *Orchestrator) resolve(ctx context.Context, log *slog.Logger, s Saga) {
	result, err := o.destination.Enquiry(ctx, s.ID)
	if err != nil {
		o.retry(ctx, log, s, err)
		return
	}
	switch result.Status {
	case Applied:
		o.complete(ctx, log, s, StateResolving, result.RemoteRef)
	case Rejected:
		o.advance(ctx, log, s, StateResolving, StateCompensating, nil, nil)
	default:
		// Still unknown. Parked, not guessed; an operator resolves it in S9.
		o.retry(ctx, log, s, errors.New("destination still says unknown"))
	}
}

func (o *Orchestrator) complete(ctx context.Context, log *slog.Logger, s Saga, from, remoteRef string) {
	event := Event{
		Type: "transfer.completed",
		Data: map[string]any{
			"transfer_id":  s.ID,
			"from_account": s.Request.FromAccount,
			"destination":  s.Request.Destination,
			"amount_minor": s.Request.AmountMinor,
			"currency":     s.Request.Currency,
			"reference":    s.Request.Reference,
			"remote_ref":   remoteRef,
		},
	}
	if _, err := o.store.AdvanceWithEvent(ctx, s.ID, from, StateCompleted,
		&remoteRef, nil, nil, event); err != nil {
		o.recordFailure(ctx, log, "complete", err)
		return
	}
	log.InfoContext(ctx, "transfer completed")
}

func (o *Orchestrator) fail(ctx context.Context, log *slog.Logger, s Saga, from, reason string) {
	event := Event{
		Type: "transfer.failed",
		Data: map[string]any{
			"transfer_id":    s.ID,
			"from_account":   s.Request.FromAccount,
			"destination":    s.Request.Destination,
			"amount_minor":   s.Request.AmountMinor,
			"currency":       s.Request.Currency,
			"reference":      s.Request.Reference,
			"failure_reason": reason,
		},
	}
	if _, err := o.store.AdvanceWithEvent(ctx, s.ID, from, StateFailed,
		nil, nil, &reason, event); err != nil {
		o.recordFailure(ctx, log, "fail", err)
		return
	}
	log.WarnContext(ctx, "transfer failed", "reason", reason)
}

func (o *Orchestrator) advance(ctx context.Context, log *slog.Logger, s Saga, from, to string, remoteRef, captureTxID *string) {
	if _, err := o.store.Advance(ctx, s.ID, from, to, remoteRef, captureTxID, nil); err != nil {
		o.recordFailure(ctx, log, "advance", err)
	}
}

func (o *Orchestrator) recordFailure(ctx context.Context, log *slog.Logger, step string, err error) {
	if errors.Is(err, ErrStale) {
		log.InfoContext(ctx, "saga moved on without us", "step", step)
		return
	}
	log.ErrorContext(ctx, "record step", "step", step, "err", err) // the lease expires: it runs again
}

func (o *Orchestrator) retry(ctx context.Context, log *slog.Logger, s Saga, cause error) {
	delay := o.backoff(s.Attempts)
	if err := o.store.Retry(ctx, s.ID, s.State, cause.Error(), delay); err != nil {
		log.ErrorContext(ctx, "schedule retry", "err", err)
	}
	log.WarnContext(ctx, "step failed, retrying", "attempt", s.Attempts+1, "in", delay, "err", cause)
}

// backoff doubles per attempt, up to a minute, with jitter.
func (o *Orchestrator) backoff(attempts int32) time.Duration {
	d := o.Backoff << min(attempts, 6)
	if d > time.Minute {
		d = time.Minute
	}
	return d/2 + rand.N(d/2+1)
}
