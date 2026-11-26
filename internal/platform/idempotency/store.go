package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// TTL is how long a key is remembered. It is longer than any client retry
// window the API documents.
const TTL = 24 * time.Hour

// Record is a stored key and, once complete, the response it produced.
type Record struct {
	ID             string
	Fingerprint    string
	Status         string // "in_progress" or "completed"
	ResponseStatus int
	ResponseBody   []byte
	ResourceID     *string
	ExpiresAt      time.Time
}

// Store keeps keys in the calling service's own database. It is the source of
// truth; a cache in front of it would be a second answer, not a faster one.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store over pool, whose database has the idempotency_keys
// table.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Begin claims scope+key for this request. Inserted is true when this call
// created the claim and the caller should execute the request; false means an
// existing record is returned for a replay, a conflict or an in-progress
// refusal. An expired record is removed and the claim is retried once.
func (s *Store) Begin(ctx context.Context, scope, key, fingerprint string, ttl time.Duration) (Record, bool, error) {
	expiresAt := time.Now().Add(ttl)
	for attempt := 0; attempt < 2; attempt++ {
		id := ulid.Make().String()
		var inserted string
		err := s.pool.QueryRow(ctx, `
			INSERT INTO idempotency_keys
				(id, idempotency_key, scope, request_fingerprint, status, created_at, expires_at)
			VALUES ($1, $2, $3, $4, 'in_progress', now(), $5)
			ON CONFLICT (scope, idempotency_key) DO NOTHING
			RETURNING id`,
			id, key, scope, fingerprint, expiresAt).Scan(&inserted)
		if err == nil {
			return Record{
				ID: inserted, Fingerprint: fingerprint, Status: "in_progress", ExpiresAt: expiresAt,
			}, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Record{}, false, fmt.Errorf("idempotency: claim %q: %w", key, err)
		}

		rec, err := s.get(ctx, scope, key)
		if err != nil {
			return Record{}, false, err
		}
		if rec.ExpiresAt.After(time.Now()) {
			return rec, false, nil
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE id = $1`, rec.ID); err != nil {
			return Record{}, false, fmt.Errorf("idempotency: drop expired %q: %w", key, err)
		}
	}
	return Record{}, false, fmt.Errorf("idempotency: could not claim key %q", key)
}

// Complete stores the response the request produced.
func (s *Store) Complete(ctx context.Context, id string, status int, body []byte, resourceID *string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed',
		    response_status = $2,
		    response_body = NULLIF($3, ''),
		    resource_id = $4
		WHERE id = $1`,
		id, status, string(body), resourceID)
	if err != nil {
		return fmt.Errorf("idempotency: complete: %w", err)
	}
	return nil
}

// Sweep deletes expired keys. It runs on a timer; the retention window is
// what makes "same key, same response" true for as long as a client may retry.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("idempotency: sweep: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) get(ctx context.Context, scope, key string) (Record, error) {
	var (
		rec  Record
		body string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, request_fingerprint, status,
		       coalesce(response_status, 0),
		       coalesce(response_body, ''),
		       resource_id, expires_at
		FROM idempotency_keys
		WHERE scope = $1 AND idempotency_key = $2`, scope, key).
		Scan(&rec.ID, &rec.Fingerprint, &rec.Status, &rec.ResponseStatus,
			&body, &rec.ResourceID, &rec.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, fmt.Errorf("idempotency: key %q vanished", key)
	}
	if err != nil {
		return Record{}, fmt.Errorf("idempotency: read %q: %w", key, err)
	}
	rec.ResponseBody = []byte(body)
	return rec, nil
}
