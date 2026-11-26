-- +goose Up
-- Stage 3: the transfer saga, its idempotency keys, and the outbox its
-- terminal events are written to. The states are the saga document's states,
-- made concrete: received -> reserved -> debited -> completed, with
-- compensating and resolving as the two non-happy paths.
BEGIN;

CREATE TABLE idempotency_keys (
    id                  TEXT        PRIMARY KEY,
    idempotency_key     TEXT        NOT NULL,
    scope               TEXT        NOT NULL,
    request_fingerprint TEXT        NOT NULL,
    status              TEXT        NOT NULL,
    response_status     INT,
    -- TEXT, not JSONB: a replay must return the response verbatim.
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

CREATE TABLE transfer_sagas (
    id             TEXT        PRIMARY KEY,
    tenant_id      TEXT        NOT NULL,
    state          TEXT        NOT NULL,
    request        JSONB       NOT NULL,
    created_by     TEXT        NOT NULL,  -- the caller that opened the saga
    correlation_id TEXT        NOT NULL,  -- the request's correlation, carried into events
    hold_id        TEXT,
    capture_tx_id  TEXT,
    attempts       INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error     TEXT,
    remote_ref     TEXT,
    failure_reason TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT transfer_sagas_state_chk CHECK (state IN (
        'received', 'reserved', 'debited', 'completed',
        'failed', 'compensating', 'resolving'
    ))
);

-- The worker claims only sagas that are due and not terminal.
CREATE INDEX transfer_sagas_due_idx
    ON transfer_sagas (next_attempt_at)
    WHERE state NOT IN ('completed', 'failed');

CREATE TABLE outbox (
    id             TEXT        PRIMARY KEY,
    tenant_id      TEXT        NOT NULL,
    aggregate_type TEXT        NOT NULL,
    aggregate_id   TEXT        NOT NULL,
    event_type     TEXT        NOT NULL,
    payload        JSONB       NOT NULL,
    headers        JSONB       NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ
);

CREATE INDEX outbox_unpublished_idx ON outbox (created_at) WHERE published_at IS NULL;

COMMIT;
