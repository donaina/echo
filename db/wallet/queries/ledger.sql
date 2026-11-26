-- name: CreateTransaction :one
INSERT INTO transactions (
    id, tenant_id, operation_type, status, occurred_at, value_date,
    correlation_id, causation_id, created_by, reverses_id, metadata
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: CreateEntry :one
INSERT INTO entries (id, tenant_id, transaction_id, account_id, delta_minor, currency)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1;

-- name: GetTransactionForUpdate :one
SELECT * FROM transactions WHERE id = $1 FOR UPDATE;

-- name: GetTransactionEntries :many
SELECT * FROM entries WHERE transaction_id = $1 ORDER BY created_at, id;

-- name: GetReversal :one
SELECT * FROM transactions WHERE reverses_id = $1;

-- name: MarkTransactionReversed :execrows
UPDATE transactions SET status = 'reversed' WHERE id = $1 AND status = 'posted';
