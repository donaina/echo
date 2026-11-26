-- name: CreateAccount :one
INSERT INTO accounts (id, tenant_id, owner_ref, account_type, currency, status)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: LockAccount :one
SELECT * FROM accounts WHERE id = $1 FOR UPDATE;

-- name: GetSystemAccount :one
SELECT * FROM accounts
WHERE tenant_id = $1 AND account_type = $2 AND currency = $3;

-- name: GetAccountBalance :one
SELECT COALESCE(SUM(delta_minor), 0)::bigint AS balance
FROM entries
WHERE account_id = $1;
