-- +goose Up
-- The recon schema is deliberately empty until S7: runs and breaks
-- arrive with core posting.
--
-- SELECT 1 gives goose an executable statement while leaving the schema empty.
SELECT 1;

-- +goose Down
SELECT 1;
