-- +goose Up
-- The transfer schema is deliberately empty until S3: saga state
-- arrives with the transfer saga.
--
-- SELECT 1 gives goose an executable statement while leaving the schema empty.
SELECT 1;

-- +goose Down
SELECT 1;
