package app

import (
	"context"
	"database/sql"
	"log"
	"time"
)

const (
	retentionDays               = 30
	retentionUsageBatchSize     = 1000
	retentionVacuumMinFreeBytes = int64(64 << 20)
	retentionCleanupInterval    = 24 * time.Hour
)

type retentionCleanupResult struct {
	UsageRecords        int64
	QuotaCharges        int64
	KeeperRuns          int64
	KeeperRunAccounts   int64
	ModelCheckerRuns    int64
	ModelCheckerRunRows int64
	Vacuumed            bool
}

func (r retentionCleanupResult) totalDeleted() int64 {
	return r.UsageRecords + r.QuotaCharges + r.KeeperRuns + r.KeeperRunAccounts + r.ModelCheckerRuns + r.ModelCheckerRunRows
}

func (a *App) startRetention(ctx context.Context) {
	a.runRetentionCleanup(ctx, "startup")

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	a.retentionCancel = cancel
	a.retentionDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(retentionCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				a.runRetentionCleanup(runCtx, "daily")
			}
		}
	}()
}

func (a *App) runRetentionCleanup(ctx context.Context, trigger string) {
	result, err := a.cleanupExpiredRecords(ctx, time.Now())
	if err != nil {
		log.Printf("retention cleanup (%s) failed after deleting usage_records=%d quota_charges=%d keeper_runs=%d keeper_run_accounts=%d model_checker_runs=%d model_checker_run_models=%d: %v",
			trigger, result.UsageRecords, result.QuotaCharges, result.KeeperRuns, result.KeeperRunAccounts, result.ModelCheckerRuns, result.ModelCheckerRunRows, err)
		return
	}
	if result.totalDeleted() == 0 && !result.Vacuumed {
		return
	}
	log.Printf("retention cleanup (%s) completed: usage_records=%d quota_charges=%d keeper_runs=%d keeper_run_accounts=%d model_checker_runs=%d model_checker_run_models=%d vacuumed=%t",
		trigger, result.UsageRecords, result.QuotaCharges, result.KeeperRuns, result.KeeperRunAccounts, result.ModelCheckerRuns, result.ModelCheckerRunRows, result.Vacuumed)
}

func (a *App) cleanupExpiredRecords(ctx context.Context, now time.Time) (retentionCleanupResult, error) {
	result := retentionCleanupResult{}
	cutoff := now.In(appTimeLocation).AddDate(0, 0, -retentionDays)
	for {
		usageDeleted, chargesDeleted, err := a.deleteExpiredUsageBatch(ctx, cutoff)
		if err != nil {
			return result, err
		}
		result.UsageRecords += usageDeleted
		result.QuotaCharges += chargesDeleted
		if usageDeleted < retentionUsageBatchSize {
			break
		}
	}

	keeperRuns, keeperAccounts, err := a.deleteExpiredKeeperHistory(ctx, cutoff)
	if err != nil {
		return result, err
	}
	result.KeeperRuns = keeperRuns
	result.KeeperRunAccounts = keeperAccounts

	checkerRuns, checkerRows, err := a.deleteExpiredModelCheckerHistory(ctx, cutoff)
	if err != nil {
		return result, err
	}
	result.ModelCheckerRuns = checkerRuns
	result.ModelCheckerRunRows = checkerRows

	vacuumed, err := a.compactRetentionSpace(ctx)
	if err != nil {
		return result, err
	}
	result.Vacuumed = vacuumed
	return result, nil
}

func (a *App) deleteExpiredUsageBatch(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	cutoffValue := dbTime(cutoff)
	chargeResult, err := tx.ExecContext(ctx, `
		DELETE FROM user_quota_charges
		WHERE usage_record_id IN (
			SELECT id FROM usage_records
			WHERE timestamp < ?
			ORDER BY timestamp, id
			LIMIT ?
		)
	`, cutoffValue, retentionUsageBatchSize)
	if err != nil {
		return 0, 0, err
	}
	chargesDeleted, _ := chargeResult.RowsAffected()

	usageResult, err := tx.ExecContext(ctx, `
		DELETE FROM usage_records
		WHERE id IN (
			SELECT id FROM usage_records
			WHERE timestamp < ?
			ORDER BY timestamp, id
			LIMIT ?
		)
	`, cutoffValue, retentionUsageBatchSize)
	if err != nil {
		return 0, 0, err
	}
	usageDeleted, _ := usageResult.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	committed = true
	return usageDeleted, chargesDeleted, nil
}

func (a *App) deleteExpiredKeeperHistory(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	cutoffValue := dbTime(cutoff)
	accountResult, err := tx.ExecContext(ctx, `
		DELETE FROM codex_keeper_run_accounts
		WHERE run_id IN (
			SELECT id FROM codex_keeper_runs WHERE started_at < ?
		)
	`, cutoffValue)
	if err != nil {
		return 0, 0, err
	}
	accountsDeleted, _ := accountResult.RowsAffected()

	runResult, err := tx.ExecContext(ctx, `DELETE FROM codex_keeper_runs WHERE started_at < ?`, cutoffValue)
	if err != nil {
		return 0, 0, err
	}
	runsDeleted, _ := runResult.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	committed = true
	return runsDeleted, accountsDeleted, nil
}

func (a *App) deleteExpiredModelCheckerHistory(ctx context.Context, cutoff time.Time) (int64, int64, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	cutoffValue := dbTime(cutoff)
	modelResult, err := deleteRowsAffected(ctx, tx, `
		DELETE FROM model_checker_run_models
		WHERE run_id IN (
			SELECT id FROM model_checker_runs WHERE started_at < ?
		)
	`, cutoffValue)
	if err != nil {
		return 0, 0, err
	}
	runResult, err := deleteRowsAffected(ctx, tx, `DELETE FROM model_checker_runs WHERE started_at < ?`, cutoffValue)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	committed = true
	return runResult, modelResult, nil
}

func deleteRowsAffected(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (a *App) compactRetentionSpace(ctx context.Context) (bool, error) {
	var pageSize int64
	if err := a.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return false, err
	}
	var freePages int64
	if err := a.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return false, err
	}
	freeBytes := pageSize * freePages
	if freeBytes < retentionVacuumMinFreeBytes {
		return false, nil
	}
	log.Printf("retention compaction: vacuuming SQLite database (%d free bytes)", freeBytes)
	if _, err := a.db.ExecContext(ctx, `VACUUM`); err != nil {
		return false, err
	}
	return true, nil
}
