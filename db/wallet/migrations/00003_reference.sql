-- +goose Up
-- Stage 2: the caller's reference travels with the transaction, so an entry
-- can be traced back to the request (or the rail instruction) that caused it.
BEGIN;
ALTER TABLE transactions ADD COLUMN reference TEXT NOT NULL DEFAULT '';
CREATE INDEX transactions_reference_idx ON transactions (reference);
COMMIT;
