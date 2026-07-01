-- +goose Up
ALTER TABLE app_settings ADD COLUMN base_path VARCHAR(200) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE app_settings DROP COLUMN base_path;
