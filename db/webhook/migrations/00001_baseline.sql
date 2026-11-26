-- +goose Up
-- The webhook schema is deliberately empty until S5:
-- subscriptions and deliveries arrive with distribution.
--
-- SELECT 1 gives goose an executable statement while leaving the schema empty.
SELECT 1;

-- +goose Down
SELECT 1;
