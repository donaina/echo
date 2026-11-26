-- +goose Up
-- The auth schema is deliberately empty until S4: users, credentials
-- and refresh tokens arrive with identity.
--
-- SELECT 1 gives goose an executable statement while leaving the schema empty.
SELECT 1;

-- +goose Down
SELECT 1;
