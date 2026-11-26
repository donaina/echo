-- +goose Up
-- Article VIII: a hold reduces available balance and never moves money. Only
-- a capture posts entries, and it posts them through the ledger like any
-- other transaction.
BEGIN;

CREATE TABLE holds (
    id             TEXT        PRIMARY KEY,  -- chosen by the caller; the idempotency key
    tenant_id      TEXT        NOT NULL,
    account_id     TEXT        NOT NULL,
    amount_minor   BIGINT      NOT NULL,
    currency       TEXT        NOT NULL,
    reference      TEXT        NOT NULL DEFAULT '',
    created_by     TEXT        NOT NULL,  -- Article XI: every money object names its author
    status         TEXT        NOT NULL DEFAULT 'open',
    transaction_id TEXT,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT holds_amount_chk CHECK (amount_minor > 0),
    CONSTRAINT holds_status_chk
        CHECK (status IN ('open', 'captured', 'released', 'expired')),
    CONSTRAINT holds_captured_chk
        CHECK (status <> 'captured' OR transaction_id IS NOT NULL),
    CONSTRAINT holds_account_fk
        FOREIGN KEY (account_id, tenant_id, currency)
        REFERENCES accounts (id, tenant_id, currency),
    CONSTRAINT holds_transaction_fk
        FOREIGN KEY (transaction_id, tenant_id)
        REFERENCES transactions (id, tenant_id)
);

-- The available-balance calculation reads only open holds.
CREATE INDEX holds_open_account_idx ON holds (account_id) WHERE status = 'open';
CREATE INDEX holds_open_expiry_idx ON holds (expires_at) WHERE status = 'open';

COMMIT;
