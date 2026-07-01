package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upAddTestAPIKey, downAddTestAPIKey)
}

func upAddTestAPIKey(ctx context.Context, db *sql.DB) error {
	// Remove last_available_keys column as it's no longer needed
	// (We now use a single test API key instead of checking all keys)

	// SQLite doesn't support DROP COLUMN directly, so we need to recreate the table
	_, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_tracked_models_new (
			model_id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			schedule_cron TEXT NOT NULL DEFAULT '0 * * * *',
			last_status TEXT,
			last_checked_at TEXT,
			last_available_at TEXT,
			first_seen_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`)
	if err != nil {
		return err
	}

	// Copy data from old table to new table
	_, err = db.ExecContext(ctx, `
		INSERT INTO model_checker_tracked_models_new
		(model_id, provider, enabled, schedule_cron, last_status,
		 last_checked_at, last_available_at, first_seen_at, created_at, updated_at)
		SELECT model_id, provider, enabled, schedule_cron, last_status,
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models;
	`)
	if err != nil {
		return err
	}

	// Drop old table
	_, err = db.ExecContext(ctx, `DROP TABLE model_checker_tracked_models;`)
	if err != nil {
		return err
	}

	// Rename new table to original name
	_, err = db.ExecContext(ctx, `
		ALTER TABLE model_checker_tracked_models_new
		RENAME TO model_checker_tracked_models;
	`)
	if err != nil {
		return err
	}

	return nil
}

func downAddTestAPIKey(ctx context.Context, db *sql.DB) error {
	// Add back last_available_keys column
	_, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_tracked_models_new (
			model_id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			schedule_cron TEXT NOT NULL DEFAULT '0 * * * *',
			last_status TEXT,
			last_available_keys TEXT,
			last_checked_at TEXT,
			last_available_at TEXT,
			first_seen_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`)
	if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO model_checker_tracked_models_new
		(model_id, provider, enabled, schedule_cron, last_status, last_available_keys,
		 last_checked_at, last_available_at, first_seen_at, created_at, updated_at)
		SELECT model_id, provider, enabled, schedule_cron, last_status, '[]',
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models;
	`)
	if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, `DROP TABLE model_checker_tracked_models;`)
	if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, `
		ALTER TABLE model_checker_tracked_models_new
		RENAME TO model_checker_tracked_models;
	`)
	return err
}
