-- +goose Up
CREATE INDEX IF NOT EXISTS ix_usage_records_created_at ON usage_records(created_at);

-- +goose Down
DROP INDEX IF EXISTS ix_usage_records_created_at;