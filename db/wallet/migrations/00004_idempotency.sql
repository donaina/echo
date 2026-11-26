-- +goose Up
-- Article V: a money write is safe to retry. The key is the intent; the
-- fingerprint proves a retry is the same intent.
BEGIN;

CREATE TABLE idempotency_keys (
    id                  TEXT        PRIMARY KEY,
    idempotency_key     TEXT        NOT NULL,
    scope               TEXT        NOT NULL,   -- caller identity + route
    request_fingerprint TEXT        NOT NULL,   -- SHA-256 over the canonical request
    status              TEXT        NOT NULL,   -- 'in_progress' | 'completed'
    response_status     INT,
    -- TEXT, not JSONB: a replay must return the response verbatim, and jsonb
    -- normalises key order and whitespace.
    response_body       TEXT,
    resource_id         TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,

    CONSTRAINT idempotency_status_chk
        CHECK (status IN ('in_progress', 'completed')),
    CONSTRAINT idempotency_response_chk
        CHECK (status <> 'completed' OR response_status IS NOT NULL)
);

CREATE UNIQUE INDEX idempotency_scope_key_uq
    ON idempotency_keys (scope, idempotency_key);
CREATE INDEX idempotency_expires_idx ON idempotency_keys (expires_at);

COMMIT;
