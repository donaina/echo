package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	apperr "github.com/donaina/echo/internal/platform/errors"
	"github.com/donaina/echo/internal/transfer/transferdb"
)

// ErrStale means the saga moved on before this worker could record its step.
// It is a normal outcome, not an error to retry.
var ErrStale = errors.New("saga moved on")

// Store persists saga state and its outbox, in the transfer service's own
// database.
type Store struct {
	pool *pgxpool.Pool
	q    *transferdb.Queries
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: transferdb.New(pool)}
}

// Create opens a saga in the received state. The saga id is also the hold id,
// so every wallet call is idempotent by the same key.
func (s *Store) Create(ctx context.Context, id, tenant, createdBy, correlationID string, req Request) (Saga, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Saga{}, fmt.Errorf("transfer: encode request: %w", err)
	}
	holdID := id
	row, err := s.q.CreateSaga(ctx, transferdb.CreateSagaParams{
		ID:            id,
		TenantID:      tenant,
		State:         StateReceived,
		Request:       payload,
		CreatedBy:     createdBy,
		CorrelationID: correlationID,
		HoldID:        &holdID,
		Attempts:      0,
	})
	if err != nil {
		return Saga{}, fmt.Errorf("transfer: create saga: %w", err)
	}
	return sagaFromRow(row)
}

// Get returns one saga.
func (s *Store) Get(ctx context.Context, id string) (Saga, error) {
	row, err := s.q.GetSaga(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Saga{}, apperr.New(apperr.TransferNotFound, "no transfer with that id")
	}
	if err != nil {
		return Saga{}, fmt.Errorf("transfer: get saga: %w", err)
	}
	return sagaFromRow(row)
}

// Claim leases due sagas to this worker. Another worker skips them until the
// lease expires, which is what makes a crashed step retry rather than vanish.
func (s *Store) Claim(ctx context.Context, limit int32, lease time.Duration) ([]Saga, error) {
	rows, err := s.q.ClaimDueSagas(ctx, transferdb.ClaimDueSagasParams{
		Limit:   limit,
		Column2: pgtype.Interval{Microseconds: lease.Microseconds(), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("transfer: claim: %w", err)
	}
	out := make([]Saga, 0, len(rows))
	for _, row := range rows {
		saga, err := sagaFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, saga)
	}
	return out, nil
}

// Advance moves a saga from one state to another, but only if it is still in
// from. A lost race is ErrStale, never a second transition.
func (s *Store) Advance(ctx context.Context, id, from, to string, remoteRef, captureTxID, failureReason *string) (Saga, error) {
	row, err := s.q.AdvanceSaga(ctx, transferdb.AdvanceSagaParams{
		ID:            id,
		State:         from,
		State_2:       to,
		RemoteRef:     remoteRef,
		CaptureTxID:   captureTxID,
		FailureReason: failureReason,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Saga{}, ErrStale
	}
	if err != nil {
		return Saga{}, fmt.Errorf("transfer: advance: %w", err)
	}
	return sagaFromRow(row)
}

// AdvanceWithEvent moves the saga and writes its terminal event in one
// database transaction (Article VI). The event and the state change are one
// fact.
func (s *Store) AdvanceWithEvent(ctx context.Context, id, from, to string, remoteRef, captureTxID, failureReason *string, event Event) (Saga, error) {
	var out Saga
	err := pgx.BeginFunc(ctx, s.pool, func(db pgx.Tx) error {
		q := s.q.WithTx(db)
		row, err := q.AdvanceSaga(ctx, transferdb.AdvanceSagaParams{
			ID:            id,
			State:         from,
			State_2:       to,
			RemoteRef:     remoteRef,
			CaptureTxID:   captureTxID,
			FailureReason: failureReason,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return err
		}
		saga, err := sagaFromRow(row)
		if err != nil {
			return err
		}
		payload, headers, err := envelope(saga, event)
		if err != nil {
			return err
		}
		if err := q.InsertOutboxEvent(ctx, transferdb.InsertOutboxEventParams{
			ID:            ulid.Make().String(),
			TenantID:      saga.TenantID,
			AggregateType: "transfer",
			AggregateID:   saga.ID,
			EventType:     event.Type,
			Payload:       payload,
			Headers:       headers,
		}); err != nil {
			return err
		}
		out = saga
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrStale) {
			return Saga{}, ErrStale
		}
		return Saga{}, fmt.Errorf("transfer: advance with event: %w", err)
	}
	return out, nil
}

// Retry schedules the next attempt with backoff and records why this one
// failed.
func (s *Store) Retry(ctx context.Context, id, from, cause string, after time.Duration) error {
	n, err := s.q.RetrySaga(ctx, transferdb.RetrySagaParams{
		ID:        id,
		State:     from,
		LastError: &cause,
		Column4:   pgtype.Interval{Microseconds: after.Microseconds(), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("transfer: retry: %w", err)
	}
	if n == 0 {
		return ErrStale
	}
	return nil
}

func sagaFromRow(row transferdb.TransferSaga) (Saga, error) {
	var req Request
	if err := json.Unmarshal(row.Request, &req); err != nil {
		return Saga{}, fmt.Errorf("transfer: decode saga request: %w", err)
	}
	return Saga{
		ID:            row.ID,
		TenantID:      row.TenantID,
		State:         row.State,
		Request:       req,
		CreatedBy:     row.CreatedBy,
		CorrelationID: row.CorrelationID,
		HoldID:        row.HoldID,
		CaptureTxID:   row.CaptureTxID,
		Attempts:      row.Attempts,
		NextAttemptAt: row.NextAttemptAt.Time,
		LastError:     row.LastError,
		RemoteRef:     row.RemoteRef,
		FailureReason: row.FailureReason,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
	}, nil
}

// envelope is the event the outbox will publish: the S5 envelope, written now
// so publishing later is a relay, not a redesign.
func envelope(s Saga, e Event) (payload, headers []byte, err error) {
	eventID := ulid.Make().String()
	envelope := map[string]any{
		"event_id":       eventID,
		"type":           e.Type,
		"occurred_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"tenant_id":      s.TenantID,
		"correlation_id": s.CorrelationID,
		"causation_id":   s.ID,
		"partition_key":  s.Request.FromAccount,
		"schema_ref":     "payments." + e.Type + ".v1",
		"data":           e.Data,
	}
	payload, err = json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}
	headers, err = json.Marshal(map[string]string{
		"correlation_id": s.CorrelationID,
		"causation_id":   s.ID,
		"schema_ref":     "payments." + e.Type + ".v1",
	})
	if err != nil {
		return nil, nil, err
	}
	return payload, headers, nil
}
