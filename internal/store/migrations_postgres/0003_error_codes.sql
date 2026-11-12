-- +goose Up
-- Postgres dialect (mirrors migrations/0003_error_codes.sql): exact HTTP status
-- codes per Cloud Run service as a JSON object stored as TEXT.
ALTER TABLE error_rows ADD COLUMN by_code_json TEXT NULL;

-- +goose Down
ALTER TABLE error_rows DROP COLUMN by_code_json;
