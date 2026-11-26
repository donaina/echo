-- name: CreateTransaction :one
INSERT INTO transactions (
    id, tenant_id, operation_type, status, occurred_at, value_date,
    correlation_id, causation_id, created_by, reverses_id, reference, metadata
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: CreateEntry :one
INSERT INTO entries (
    id, tenant_id, transaction_id, account_id, delta_minor, currency, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
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

-- name: ListWalletEntriesFirst :many
SELECT
    e.id, e.tenant_id, e.transaction_id, e.account_id, e.delta_minor,
    e.currency, e.created_at,
    t.operation_type, t.reference,
    COALESCE((SELECT e2.account_id FROM entries e2
      WHERE e2.transaction_id = e.transaction_id AND e2.account_id <> e.account_id
      ORDER BY e2.id LIMIT 1)::text, '') AS counterparty
FROM entries e
JOIN transactions t ON t.id = e.transaction_id
WHERE e.account_id = $1
ORDER BY e.created_at DESC, e.id DESC
LIMIT $2;

-- name: ListWalletEntriesAfter :many
SELECT
    e.id, e.tenant_id, e.transaction_id, e.account_id, e.delta_minor,
    e.currency, e.created_at,
    t.operation_type, t.reference,
    COALESCE((SELECT e2.account_id FROM entries e2
      WHERE e2.transaction_id = e.transaction_id AND e2.account_id <> e.account_id
      ORDER BY e2.id LIMIT 1)::text, '') AS counterparty
FROM entries e
JOIN transactions t ON t.id = e.transaction_id
WHERE e.account_id = $1
  AND (e.created_at < $2 OR (e.created_at = $2 AND e.id < $3))
ORDER BY e.created_at DESC, e.id DESC
LIMIT $4;
