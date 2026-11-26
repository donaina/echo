-- name: CreateSaga :one
INSERT INTO transfer_sagas (
    id, tenant_id, state, request, created_by, correlation_id, hold_id, attempts, next_attempt_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
RETURNING *;

-- name: GetSaga :one
SELECT * FROM transfer_sagas WHERE id = $1;

-- name: ClaimDueSagas :many
WITH due AS (
    SELECT id FROM transfer_sagas
    WHERE state NOT IN ('completed', 'failed') AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE transfer_sagas s
SET next_attempt_at = now() + $2::interval, updated_at = now()
FROM due
WHERE s.id = due.id
RETURNING s.*;

-- name: AdvanceSaga :one
UPDATE transfer_sagas
SET state = $3,
    remote_ref = COALESCE($4, remote_ref),
    capture_tx_id = COALESCE($5, capture_tx_id),
    failure_reason = COALESCE($6, failure_reason),
    last_error = NULL,
    attempts = 0,
    next_attempt_at = now(),
    updated_at = now()
WHERE id = $1 AND state = $2
RETURNING *;

-- name: RetrySaga :execrows
UPDATE transfer_sagas
SET attempts = attempts + 1,
    last_error = $3,
    next_attempt_at = now() + $4::interval,
    updated_at = now()
WHERE id = $1 AND state = $2;

-- name: InsertOutboxEvent :exec
INSERT INTO outbox (id, tenant_id, aggregate_type, aggregate_id, event_type, payload, headers)
VALUES ($1, $2, $3, $4, $5, sqlc.arg(payload)::jsonb, sqlc.arg(headers)::jsonb);
