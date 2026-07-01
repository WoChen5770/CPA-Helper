-- +goose Up
ALTER TABLE app_settings ADD COLUMN base_path VARCHAR(200) NOT NULL DEFAULT '';
ALTER TABLE app_settings ADD COLUMN cpamc_url VARCHAR(500) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE app_settings DROP COLUMN cpamc_url;
ALTER TABLE app_settings DROP COLUMN base_path;
