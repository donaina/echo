-- name: CreateHold :one
INSERT INTO holds (id, tenant_id, account_id, amount_minor, currency, reference, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetHold :one
SELECT * FROM holds WHERE id = $1;

-- name: LockHold :one
SELECT * FROM holds WHERE id = $1 FOR UPDATE;

-- name: SettleHold :one
UPDATE holds
SET status = $2, transaction_id = $3, updated_at = now()
WHERE id = $1 AND status = 'open'
RETURNING *;

-- name: OpenHoldTotal :one
SELECT COALESCE(SUM(amount_minor), 0)::bigint AS total
FROM holds
WHERE account_id = $1 AND status = 'open';

-- name: ExpireHolds :many
UPDATE holds
SET status = 'expired', updated_at = now()
WHERE status = 'open' AND expires_at <= now()
RETURNING id, account_id;
