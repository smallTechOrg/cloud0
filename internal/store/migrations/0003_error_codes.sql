-- +goose Up
-- Exact HTTP status codes per Cloud Run service (errors explainability): a JSON
-- object {"404": 1463, "502": 2} stored as TEXT. NULL for rows written before
-- this migration and for sources that do not produce per-code counts.
ALTER TABLE error_rows ADD COLUMN by_code_json TEXT NULL;

-- +goose Down
ALTER TABLE error_rows DROP COLUMN by_code_json;
