package app

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCleanupExpiredRecords(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions failed: %v", err)
	}
	defer app.Close()

	now := time.Date(2026, 11, 17, 12, 0, 0, 0, appTimeLocation)
	oldTime := now.AddDate(0, 0, -31)
	freshTime := now.AddDate(0, 0, -30)

	userResult, err := app.db.Exec(`INSERT INTO users (username, created_at, updated_at) VALUES ('member', ?, ?)`, dbTime(now), dbTime(now))
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := userResult.LastInsertId()
	oldUsageID := insertRetentionUsage(t, app, oldTime, "old-usage")
	freshUsageID := insertRetentionUsage(t, app, freshTime, "fresh-usage")
	insertRetentionQuotaCharge(t, app, oldUsageID, userID, oldTime)
	insertRetentionQuotaCharge(t, app, freshUsageID, userID, freshTime)

	oldKeeperRunID, oldKeeperAccountID := insertRetentionKeeperRun(t, app, oldTime)
	freshKeeperRunID, freshKeeperAccountID := insertRetentionKeeperRun(t, app, freshTime)
	oldCheckerRunID, oldCheckerRowID := insertRetentionModelCheckerRun(t, app, oldTime)
	freshCheckerRunID, freshCheckerRowID := insertRetentionModelCheckerRun(t, app, freshTime)

	result, err := app.cleanupExpiredRecords(context.Background(), now)
	if err != nil {
		t.Fatalf("cleanupExpiredRecords failed: %v", err)
	}
	if result.UsageRecords != 1 || result.QuotaCharges != 1 {
		t.Fatalf("usage cleanup = %+v, want 1 usage record and 1 quota charge", result)
	}
	if result.KeeperRuns != 1 || result.KeeperRunAccounts != 1 {
		t.Fatalf("keeper cleanup = %+v, want 1 run and 1 account row", result)
	}
	if result.ModelCheckerRuns != 1 || result.ModelCheckerRunRows != 1 {
		t.Fatalf("model checker cleanup = %+v, want 1 run and 1 model row", result)
	}

	assertRetentionCount(t, app, `SELECT COUNT(*) FROM usage_records WHERE id = ?`, 0, oldUsageID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM usage_records WHERE id = ?`, 1, freshUsageID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM user_quota_charges WHERE usage_record_id IN (?, ?)`, 1, oldUsageID, freshUsageID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM codex_keeper_runs WHERE id = ?`, 0, oldKeeperRunID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM codex_keeper_runs WHERE id = ?`, 1, freshKeeperRunID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM codex_keeper_run_accounts WHERE id IN (?, ?)`, 1, oldKeeperAccountID, freshKeeperAccountID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM model_checker_runs WHERE id = ?`, 0, oldCheckerRunID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM model_checker_runs WHERE id = ?`, 1, freshCheckerRunID)
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM model_checker_run_models WHERE id IN (?, ?)`, 1, oldCheckerRowID, freshCheckerRowID)
}

func TestCleanupExpiredUsageRecordsDeletesAllBatches(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions failed: %v", err)
	}
	defer app.Close()

	now := time.Date(2026, 11, 17, 12, 0, 0, 0, appTimeLocation)
	oldTime := now.AddDate(0, 0, -31)
	tx, err := app.db.Begin()
	if err != nil {
		t.Fatalf("begin seed transaction: %v", err)
	}
	statement, err := tx.Prepare(`INSERT INTO usage_records (created_at, timestamp, dedupe_key, raw_json) VALUES (?, ?, ?, '{}')`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare usage insert: %v", err)
	}
	for index := 0; index < retentionUsageBatchSize+1; index++ {
		if _, err := statement.Exec(dbTime(oldTime), dbTime(oldTime), fmt.Sprintf("old-batch-%d", index)); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			t.Fatalf("insert old usage %d: %v", index, err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close usage insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit usage seed: %v", err)
	}

	result, err := app.cleanupExpiredRecords(context.Background(), now)
	if err != nil {
		t.Fatalf("cleanupExpiredRecords failed: %v", err)
	}
	if result.UsageRecords != retentionUsageBatchSize+1 {
		t.Fatalf("deleted usage records = %d, want %d", result.UsageRecords, retentionUsageBatchSize+1)
	}
	assertRetentionCount(t, app, `SELECT COUNT(*) FROM usage_records`, 0)
}

func TestStartupRetentionDeletesExpiredRecords(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("initial NewWithOptions failed: %v", err)
	}
	insertRetentionUsage(t, app, time.Now().In(appTimeLocation).AddDate(0, 0, -31), "startup-old-usage")
	app.Close()

	started, err := NewWithOptions(context.Background(), NewOptions{RequireReady: true, StartBackground: true})
	if err != nil {
		t.Fatalf("startup NewWithOptions failed: %v", err)
	}
	defer started.Close()
	assertRetentionCount(t, started, `SELECT COUNT(*) FROM usage_records`, 0)
}

func insertRetentionUsage(t *testing.T, app *App, at time.Time, dedupeKey string) int64 {
	t.Helper()
	result, err := app.db.Exec(`
		INSERT INTO usage_records (created_at, timestamp, dedupe_key, raw_json)
		VALUES (?, ?, ?, '{}')
	`, dbTime(at), dbTime(at), dedupeKey)
	if err != nil {
		t.Fatalf("insert usage record %q: %v", dedupeKey, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("usage record id: %v", err)
	}
	return id
}

func insertRetentionQuotaCharge(t *testing.T, app *App, usageRecordID, userID int64, at time.Time) {
	t.Helper()
	_, err := app.db.Exec(`
		INSERT INTO user_quota_charges (
			usage_record_id, user_id, usage_username, amount_usd,
			monthly_deducted_usd, lifetime_deducted_usd, unpriced,
			quota_month, created_at
		) VALUES (?, ?, 'member', 1, 1, 0, 0, ?, ?)
	`, usageRecordID, userID, at.Format("2006-01"), dbTime(at))
	if err != nil {
		t.Fatalf("insert quota charge: %v", err)
	}
}

func insertRetentionKeeperRun(t *testing.T, app *App, at time.Time) (int64, int64) {
	t.Helper()
	result, err := app.db.Exec(`
		INSERT INTO codex_keeper_runs (mode, state, detail, started_at, created_at, updated_at)
		VALUES ('manual', 'success', '', ?, ?, ?)
	`, dbTime(at), dbTime(at), dbTime(at))
	if err != nil {
		t.Fatalf("insert keeper run: %v", err)
	}
	runID, _ := result.LastInsertId()
	result, err = app.db.Exec(`
		INSERT INTO codex_keeper_run_accounts (run_id, auth_name, result, checked_at, created_at)
		VALUES (?, 'account.json', 'healthy', ?, ?)
	`, runID, dbTime(at), dbTime(at))
	if err != nil {
		t.Fatalf("insert keeper run account: %v", err)
	}
	accountID, _ := result.LastInsertId()
	return runID, accountID
}

func insertRetentionModelCheckerRun(t *testing.T, app *App, at time.Time) (int64, int64) {
	t.Helper()
	result, err := app.db.Exec(`
		INSERT INTO model_checker_runs (mode, state, detail, started_at, finished_at, created_at, updated_at)
		VALUES ('manual', 'success', '', ?, ?, ?, ?)
	`, dbTime(at), dbTime(at), dbTime(at), dbTime(at))
	if err != nil {
		t.Fatalf("insert model checker run: %v", err)
	}
	runID, _ := result.LastInsertId()
	result, err = app.db.Exec(`
		INSERT INTO model_checker_run_models (run_id, model_id, provider, status, checked_at, created_at)
		VALUES (?, 'gpt-test', 'openai', 'available', ?, ?)
	`, runID, dbTime(at), dbTime(at))
	if err != nil {
		t.Fatalf("insert model checker run model: %v", err)
	}
	rowID, _ := result.LastInsertId()
	return runID, rowID
}

func assertRetentionCount(t *testing.T, app *App, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := app.db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query retention count: %v", err)
	}
	if got != want {
		t.Fatalf("retention count = %d, want %d for %q", got, want, query)
	}
}
