package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(upCPAMCURL, nil)
}

func upCPAMCURL(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	cols, err := tableColumns(ctx, tx, "app_settings")
	if err != nil {
		return err
	}
	if !cols["cpamc_url"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE app_settings ADD COLUMN cpamc_url VARCHAR(1000) NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
