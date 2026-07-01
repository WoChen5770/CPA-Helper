package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upRefactorModelChecker, downRefactorModelChecker)
}

func upRefactorModelChecker(ctx context.Context, db *sql.DB) error {
	// Add schedule_cron column to model_checker_tracked_models
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE model_checker_tracked_models
		ADD COLUMN schedule_cron TEXT DEFAULT '0 * * * *';
	`); err != nil {
		return err
	}

	// Remove per-model configuration columns (use global config instead)
	// SQLite doesn't support DROP COLUMN directly, so we need to recreate the table
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_tracked_models_new (
			model_id TEXT PRIMARY KEY,
			provider TEXT,
			enabled INTEGER DEFAULT 1,
			schedule_cron TEXT DEFAULT '0 * * * *',
			last_status TEXT,
			last_available_keys TEXT,
			last_checked_at TEXT,
			last_available_at TEXT,
			first_seen_at TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		);
	`); err != nil {
		return err
	}

	// Copy data from old table
	if _, err := db.ExecContext(ctx, `
		INSERT INTO model_checker_tracked_models_new
			(model_id, provider, enabled, schedule_cron, last_status, last_available_keys,
			 last_checked_at, last_available_at, first_seen_at, created_at, updated_at)
		SELECT model_id, provider, enabled, '0 * * * *', last_status, last_available_keys,
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models;
	`); err != nil {
		return err
	}

	// Drop old table and rename new one
	if _, err := db.ExecContext(ctx, `DROP TABLE model_checker_tracked_models;`); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
		ALTER TABLE model_checker_tracked_models_new
		RENAME TO model_checker_tracked_models;
	`); err != nil {
		return err
	}

	return nil
}

func downRefactorModelChecker(ctx context.Context, db *sql.DB) error {
	// Recreate old table structure
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_tracked_models_old (
			model_id TEXT PRIMARY KEY,
			provider TEXT,
			enabled INTEGER DEFAULT 1,
			check_interval_minutes INTEGER DEFAULT 60,
			timeout_seconds INTEGER DEFAULT 30,
			max_retries INTEGER DEFAULT 2,
			alert_on_unavailable INTEGER DEFAULT 1,
			last_status TEXT,
			last_available_keys TEXT,
			last_checked_at TEXT,
			last_available_at TEXT,
			first_seen_at TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		);
	`); err != nil {
		return err
	}

	// Copy data back
	if _, err := db.ExecContext(ctx, `
		INSERT INTO model_checker_tracked_models_old
			(model_id, provider, enabled, last_status, last_available_keys,
			 last_checked_at, last_available_at, first_seen_at, created_at, updated_at)
		SELECT model_id, provider, enabled, last_status, last_available_keys,
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models;
	`); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE model_checker_tracked_models;`); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
		ALTER TABLE model_checker_tracked_models_old
		RENAME TO model_checker_tracked_models;
	`); err != nil {
		return err
	}

	return nil
}
