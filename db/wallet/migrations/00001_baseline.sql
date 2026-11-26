-- +goose Up
-- The wallet schema is deliberately empty until S1: accounts,
-- transactions and entries are Stage 1's job, and a guessed schema is worse
-- than none.
--
-- SELECT 1 gives goose an executable statement while leaving the schema empty.
SELECT 1;

-- +goose Down
SELECT 1;
