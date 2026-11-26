-- +goose Up
-- The notification schema is deliberately empty until S5:
-- delivery state arrives with distribution.
--
-- SELECT 1 gives goose an executable statement while leaving the schema empty.
SELECT 1;

-- +goose Down
SELECT 1;
