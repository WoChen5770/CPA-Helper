package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upModelChecker, downModelChecker)
}

func upModelChecker(ctx context.Context, db *sql.DB) error {
	// Add model_checker_settings column to app_settings table
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE app_settings
		ADD COLUMN model_checker_settings TEXT DEFAULT '{}';
	`); err != nil {
		return err
	}

	// Create model_checker_tracked_models table
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_tracked_models (
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

	// Create model_checker_runs table
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			mode TEXT NOT NULL,
			state TEXT NOT NULL,
			detail TEXT,
			started_at TEXT,
			finished_at TEXT,
			total_models INTEGER DEFAULT 0,
			available_models INTEGER DEFAULT 0,
			unavailable_models INTEGER DEFAULT 0,
			newly_available INTEGER DEFAULT 0,
			newly_unavailable INTEGER DEFAULT 0,
			error_models INTEGER DEFAULT 0,
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now'))
		);
	`); err != nil {
		return err
	}

	// Create model_checker_run_models table
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE model_checker_run_models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL,
			model_id TEXT NOT NULL,
			provider TEXT,
			status TEXT NOT NULL,
			available_keys TEXT,
			error_message TEXT,
			change_type TEXT,
			checked_at TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			FOREIGN KEY (run_id) REFERENCES model_checker_runs(id) ON DELETE CASCADE
		);
	`); err != nil {
		return err
	}

	// Create indexes
	if _, err := db.ExecContext(ctx, `
		CREATE INDEX idx_model_checker_run_models_run_id
		ON model_checker_run_models(run_id);
	`); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
		CREATE INDEX idx_model_checker_run_models_model_id
		ON model_checker_run_models(model_id);
	`); err != nil {
		return err
	}

	return nil
}

func downModelChecker(ctx context.Context, db *sql.DB) error {
	// Drop tables in reverse order
	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS model_checker_run_models;`); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS model_checker_runs;`); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS model_checker_tracked_models;`); err != nil {
		return err
	}

	// Remove column from app_settings
	// SQLite doesn't support DROP COLUMN directly, so we skip it in down migration
	// The column will remain but unused

	return nil
}
