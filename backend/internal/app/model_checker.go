package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	modelCheckerLogFilePrefix    = "model-checker-"
	modelCheckerLogComponent     = "model_checker"
	modelCheckerLogRetainedFiles = 3
	modelCheckerMaxInMemoryLogs  = 300
)

// ModelCheckRunner manages the model checking daemon and runs
type ModelCheckRunner struct {
	app            *App
	mu             sync.Mutex
	running        bool
	runningModes   map[string]struct{}
	state          string
	detail         string
	mode           *string
	lastStartedAt  *time.Time
	lastFinishedAt *time.Time
	stats          modelCheckStats
	logs           []string
	cron           *cron.Cron
	cronEntries    map[string]cron.EntryID // modelID -> cron entry ID
}

type modelCheckStats struct {
	TotalModels       int `json:"total_models"`
	AvailableModels   int `json:"available_models"`
	UnavailableModels int `json:"unavailable_models"`
	NewlyAvailable    int `json:"newly_available"`
	NewlyUnavailable  int `json:"newly_unavailable"`
	ErrorModels       int `json:"error_models"`
}

type modelCheckStatusResponse struct {
	Running        bool            `json:"running"`
	RunningModes   []string        `json:"running_modes"`
	DaemonRunning  bool            `json:"daemon_running"`
	State          string          `json:"state"`
	Detail         string          `json:"detail"`
	Mode           *string         `json:"mode"`
	LastStartedAt  *string         `json:"last_started_at"`
	LastFinishedAt *string         `json:"last_finished_at"`
	Stats          modelCheckStats `json:"stats"`
	Logs           []string        `json:"logs"`
}

type ModelCheckerConfig struct {
	TimeoutSeconds int      `json:"timeout_seconds"`
	TestAPIKey     string   `json:"test_api_key"`
	TestQuestions  []string `json:"test_questions"`
}

type modelCheckerSettingsUpdateRequest struct {
	TimeoutSeconds *int      `json:"timeout_seconds"`
	TestAPIKey     *string   `json:"test_api_key"`
	TestQuestions  *[]string `json:"test_questions"`
}

type trackedModel struct {
	ModelID         string  `json:"model_id"`
	Provider        string  `json:"provider"`
	Enabled         bool    `json:"enabled"`
	ScheduleCron    string  `json:"schedule_cron"`
	LastStatus      *string `json:"last_status"`
	LastCheckedAt   *string `json:"last_checked_at"`
	LastAvailableAt *string `json:"last_available_at"`
	NextRunAt       *string `json:"next_run_at"`
	FirstSeenAt     *string `json:"first_seen_at"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type addTrackedModelRequest struct {
	ModelID      string  `json:"model_id"`
	Provider     string  `json:"provider"`
	ScheduleCron *string `json:"schedule_cron"`
}

type updateTrackedModelRequest struct {
	Enabled      *bool   `json:"enabled"`
	ScheduleCron *string `json:"schedule_cron"`
}

type checkModelResult struct {
	ModelID      string
	Provider     string
	Status       string
	ErrorMessage string
	ChangeType   string
}

func newModelCheckRunner(app *App) *ModelCheckRunner {
	return &ModelCheckRunner{
		app:          app,
		runningModes: make(map[string]struct{}),
		cronEntries:  make(map[string]cron.EntryID),
		state:        "idle",
		logs:         make([]string, 0, modelCheckerMaxInMemoryLogs),
		cron:         cron.New(cron.WithLocation(appTimeLocation)),
	}
}

// LoadAndStartSchedules loads all enabled models and starts their schedules
func (r *ModelCheckRunner) LoadAndStartSchedules(ctx context.Context) {
	// Run asynchronously to avoid blocking application startup
	go func() {
		rows, err := r.app.db.QueryContext(ctx, `
			SELECT model_id, schedule_cron
			FROM model_checker_tracked_models
			WHERE enabled = 1
		`)
		if err != nil {
			slog.Error("Failed to load enabled models for auto-start", "error", err)
			return
		}
		defer rows.Close()

		var modelsToStart []struct {
			ModelID      string
			ScheduleCron string
		}

		for rows.Next() {
			var modelID, scheduleCron string
			if err := rows.Scan(&modelID, &scheduleCron); err != nil {
				slog.Error("Failed to scan model row", "error", err)
				continue
			}
			modelsToStart = append(modelsToStart, struct {
				ModelID      string
				ScheduleCron string
			}{modelID, scheduleCron})
		}

		if len(modelsToStart) == 0 {
			return
		}

		r.logf("自动启动 %d 个已启用模型的调度", len(modelsToStart))

		for _, m := range modelsToStart {
			if err := r.StartModelSchedule(ctx, m.ModelID); err != nil {
				slog.Warn("Failed to auto-start schedule for model", "model_id", m.ModelID, "error", err)
			}
		}
	}()
}

// Status returns the current status of the runner
func (r *ModelCheckRunner) Status() modelCheckStatusResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	modes := make([]string, 0, len(r.runningModes))
	for mode := range r.runningModes {
		modes = append(modes, mode)
	}
	sort.Strings(modes)

	var lastStarted, lastFinished *string
	if r.lastStartedAt != nil {
		s := r.lastStartedAt.UTC().Format(time.RFC3339)
		lastStarted = &s
	}
	if r.lastFinishedAt != nil {
		s := r.lastFinishedAt.UTC().Format(time.RFC3339)
		lastFinished = &s
	}

	return modelCheckStatusResponse{
		Running:        r.running,
		RunningModes:   modes,
		DaemonRunning:  r.cron != nil && len(r.cron.Entries()) > 0,
		State:          r.state,
		Detail:         r.detail,
		Mode:           r.mode,
		LastStartedAt:  lastStarted,
		LastFinishedAt: lastFinished,
		Stats:          r.stats,
		Logs:           append([]string{}, r.logs...),
	}
}

func (r *ModelCheckRunner) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().In(appTimeLocation)
	timestamp := now.Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s", timestamp, message)

	r.logs = append(r.logs, line)
	if len(r.logs) > modelCheckerMaxInMemoryLogs {
		r.logs = r.logs[len(r.logs)-modelCheckerMaxInMemoryLogs:]
	}

	slog.Info(message, "component", modelCheckerLogComponent)

	// Write to file
	if logFile := r.currentLogFile(); logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			defer f.Close()
			fmt.Fprintln(f, line)
		}
	}
}

func (r *ModelCheckRunner) currentLogFile() string {
	logDir := filepath.Join(r.app.dataDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return ""
	}
	date := time.Now().In(appTimeLocation).Format("2006-01-02")
	return filepath.Join(logDir, fmt.Sprintf("%s%s.log", modelCheckerLogFilePrefix, date))
}

func (r *ModelCheckRunner) ClearLogs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = make([]string, 0, modelCheckerMaxInMemoryLogs)
}

// Load configuration from database
func (a *App) loadModelCheckerConfig(ctx context.Context) (ModelCheckerConfig, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT model_checker_settings FROM app_settings WHERE id = 1
	`)
	var settingsJSON string
	if err := row.Scan(&settingsJSON); err != nil {
		return ModelCheckerConfig{}, err
	}

	var cfg ModelCheckerConfig
	if settingsJSON != "" && settingsJSON != "{}" {
		if err := json.Unmarshal([]byte(settingsJSON), &cfg); err != nil {
			return ModelCheckerConfig{}, err
		}
	}

	// Set defaults
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 30
	}

	return cfg, nil
}

// Save configuration to database
func (a *App) saveModelCheckerConfig(ctx context.Context, cfg ModelCheckerConfig) error {
	settingsJSON, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	_, err = a.db.ExecContext(ctx, `
		UPDATE app_settings
		SET model_checker_settings = ?, updated_at = datetime('now')
		WHERE id = 1
	`, string(settingsJSON))
	return err
}

// HTTP handlers
func (a *App) handleModelChecker(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.adminUser(r.Context(), r); err != nil {
		return err
	}
	parts := splitPath(r.URL.Path, "/api/model-checker/")
	if len(parts) == 0 {
		return notFoundError("Not Found")
	}

	switch {
	case len(parts) == 1 && parts[0] == "settings":
		if r.Method == http.MethodGet {
			cfg, err := a.loadModelCheckerConfig(r.Context())
			if err != nil {
				return err
			}
			// Mask the test API key for security
			if cfg.TestAPIKey != "" {
				cfg.TestAPIKey = maskSecret(&cfg.TestAPIKey)
			}
			writeJSON(w, http.StatusOK, cfg)
			return nil
		}
		if r.Method == http.MethodPut {
			return a.updateModelCheckerSettings(w, r)
		}
		return methodNotAllowed()
	case len(parts) == 1 && parts[0] == "status":
		if err := requireMethod(r, http.MethodGet); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, a.modelCheckRunner.Status())
		return nil
	case len(parts) == 2 && parts[0] == "logs" && parts[1] == "clear":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		a.modelCheckRunner.ClearLogs()
		writeJSON(w, http.StatusOK, map[string]string{"message": "日志已清除"})
		return nil
	case len(parts) == 1 && parts[0] == "models":
		if r.Method == http.MethodGet {
			return a.getTrackedModels(w, r)
		}
		if r.Method == http.MethodPost {
			return a.addTrackedModel(w, r)
		}
		return methodNotAllowed()
	case len(parts) >= 2 && parts[0] == "models":
		modelID := strings.Join(parts[1:], "/")
		if strings.HasSuffix(modelID, "/check") {
			if err := requireMethod(r, http.MethodPost); err != nil {
				return err
			}
			return a.checkTrackedModel(w, r, strings.TrimSuffix(modelID, "/check"))
		}
		if strings.HasSuffix(modelID, "/start") {
			if err := requireMethod(r, http.MethodPost); err != nil {
				return err
			}
			return a.startModelSchedule(w, r, strings.TrimSuffix(modelID, "/start"))
		}
		if strings.HasSuffix(modelID, "/stop") {
			if err := requireMethod(r, http.MethodPost); err != nil {
				return err
			}
			return a.stopModelSchedule(w, r, strings.TrimSuffix(modelID, "/stop"))
		}
		if r.Method == http.MethodGet {
			return a.getTrackedModel(w, r, modelID)
		}
		if r.Method == http.MethodPut {
			return a.updateTrackedModel(w, r, modelID)
		}
		if r.Method == http.MethodDelete {
			return a.deleteTrackedModel(w, r, modelID)
		}
		return methodNotAllowed()
	}
	return notFoundError("Not Found")
}

func (a *App) updateModelCheckerSettings(w http.ResponseWriter, r *http.Request) error {
	var payload modelCheckerSettingsUpdateRequest
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}

	cfg, err := a.loadModelCheckerConfig(r.Context())
	if err != nil {
		return err
	}

	if payload.TimeoutSeconds != nil {
		if *payload.TimeoutSeconds < 1 {
			return validationError("timeout_seconds 不能小于 1")
		}
		cfg.TimeoutSeconds = *payload.TimeoutSeconds
	}
	if payload.TestAPIKey != nil {
		cfg.TestAPIKey = *payload.TestAPIKey
	}
	if payload.TestQuestions != nil {
		cfg.TestQuestions = *payload.TestQuestions
	}

	if err := a.saveModelCheckerConfig(r.Context(), cfg); err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, cfg)
	return nil
}

func (a *App) getTrackedModels(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT model_id, provider, enabled, schedule_cron, last_status,
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models
		ORDER BY model_id
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	models := []trackedModel{}
	for rows.Next() {
		var m trackedModel
		var lastStatus sql.NullString
		var enabledInt int
		if err := rows.Scan(
			&m.ModelID, &m.Provider, &enabledInt, &m.ScheduleCron,
			&lastStatus, &m.LastCheckedAt, &m.LastAvailableAt,
			&m.FirstSeenAt, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return err
		}
		m.Enabled = enabledInt == 1
		if lastStatus.Valid {
			m.LastStatus = &lastStatus.String
		}

		// Calculate next run time from cron schedule
		if entryID, exists := a.modelCheckRunner.cronEntries[m.ModelID]; exists {
			if entry := a.modelCheckRunner.cron.Entry(entryID); entry.ID != 0 {
				nextRun := entry.Next.Format(time.RFC3339)
				m.NextRunAt = &nextRun
			}
		}

		models = append(models, m)
	}

	writeJSON(w, http.StatusOK, models)
	return nil
}

func (a *App) addTrackedModel(w http.ResponseWriter, r *http.Request) error {
	var payload addTrackedModelRequest
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}

	if payload.ModelID == "" {
		return validationError("model_id 不能为空")
	}

	// Set defaults
	scheduleCron := "0 * * * *"
	if payload.ScheduleCron != nil {
		scheduleCron = *payload.ScheduleCron
	}

	// Validate cron expression
	if _, err := cron.ParseStandard(scheduleCron); err != nil {
		return validationError("无效的 cron 表达式: " + err.Error())
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.ExecContext(r.Context(), `
		INSERT INTO model_checker_tracked_models
		(model_id, provider, enabled, schedule_cron, first_seen_at, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?, ?)
	`, payload.ModelID, payload.Provider, scheduleCron, now, now, now)
	if err != nil {
		return err
	}

	// Auto-start schedule for this model
	if err := a.modelCheckRunner.StartModelSchedule(r.Context(), payload.ModelID); err != nil {
		slog.Warn("Failed to auto-start schedule for new model", "model_id", payload.ModelID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "模型已添加到监控"})
	return nil
}

func (a *App) getTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	var m trackedModel
	var lastStatus sql.NullString
	var enabledInt int
	err := a.db.QueryRowContext(r.Context(), `
		SELECT model_id, provider, enabled, schedule_cron, last_status,
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models
		WHERE model_id = ?
	`, modelID).Scan(
		&m.ModelID, &m.Provider, &enabledInt, &m.ScheduleCron,
		&lastStatus, &m.LastCheckedAt, &m.LastAvailableAt,
		&m.FirstSeenAt, &m.CreatedAt, &m.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return notFoundError("模型未找到")
	}
	if err != nil {
		return err
	}

	m.Enabled = enabledInt == 1
	if lastStatus.Valid {
		m.LastStatus = &lastStatus.String
	}

	writeJSON(w, http.StatusOK, m)
	return nil
}

func (a *App) updateTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	var payload updateTrackedModelRequest
	if err := decodeJSON(r, &payload); err != nil {
		return err
	}

	updates := []string{}
	args := []any{}

	if payload.Enabled != nil {
		updates = append(updates, "enabled = ?")
		if *payload.Enabled {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if payload.ScheduleCron != nil {
		// Validate cron expression
		if _, err := cron.ParseStandard(*payload.ScheduleCron); err != nil {
			return validationError("无效的 cron 表达式: " + err.Error())
		}
		updates = append(updates, "schedule_cron = ?")
		args = append(args, *payload.ScheduleCron)
	}

	if len(updates) == 0 {
		return validationError("没有要更新的字段")
	}

	updates = append(updates, "updated_at = datetime('now')")
	args = append(args, modelID)

	query := fmt.Sprintf("UPDATE model_checker_tracked_models SET %s WHERE model_id = ?",
		strings.Join(updates, ", "))
	_, err := a.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		return err
	}

	// If schedule_cron was updated, restart the schedule
	if payload.ScheduleCron != nil {
		a.modelCheckRunner.StopModelSchedule(modelID)
		if err := a.modelCheckRunner.StartModelSchedule(r.Context(), modelID); err != nil {
			slog.Warn("Failed to restart schedule after update", "model_id", modelID, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "配置已更新"})
	return nil
}

func (a *App) deleteTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	// Stop schedule first
	a.modelCheckRunner.StopModelSchedule(modelID)

	_, err := a.db.ExecContext(r.Context(), `
		DELETE FROM model_checker_tracked_models WHERE model_id = ?
	`, modelID)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "模型已从监控移除"})
	return nil
}

func (a *App) checkTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	go a.modelCheckRunner.CheckSingleModelNow(modelID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "已开始巡检"})
	return nil
}

func (a *App) startModelSchedule(w http.ResponseWriter, r *http.Request, modelID string) error {
	if err := a.modelCheckRunner.StartModelSchedule(r.Context(), modelID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "调度已启动"})
	return nil
}

func (a *App) stopModelSchedule(w http.ResponseWriter, r *http.Request, modelID string) error {
	a.modelCheckRunner.StopModelSchedule(modelID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "调度已停止"})
	return nil
}

// Core checking logic - RunOnce performs a single check run for all enabled models

// StartModelSchedule starts the cron schedule for a specific model
func (r *ModelCheckRunner) StartModelSchedule(ctx context.Context, modelID string) error {
	r.mu.Lock()

	// Check if already scheduled
	if _, exists := r.cronEntries[modelID]; exists {
		r.mu.Unlock()
		return fmt.Errorf("模型 %s 的调度已在运行", modelID)
	}

	// Load model configuration
	var model trackedModel
	var enabledInt int
	err := r.app.db.QueryRowContext(ctx, `
		SELECT model_id, provider, enabled, schedule_cron
		FROM model_checker_tracked_models
		WHERE model_id = ?
	`, modelID).Scan(&model.ModelID, &model.Provider, &enabledInt, &model.ScheduleCron)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("加载模型配置失败: %w", err)
	}
	model.Enabled = enabledInt == 1

	if !model.Enabled {
		r.mu.Unlock()
		return fmt.Errorf("模型 %s 未启用", modelID)
	}

	// Add cron job
	entryID, err := r.cron.AddFunc(model.ScheduleCron, func() {
		r.CheckSingleModel(modelID)
	})
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("添加 Cron 任务失败: %w", err)
	}

	r.cronEntries[modelID] = entryID
	shouldStart := len(r.cronEntries) == 1

	r.mu.Unlock()

	// Start cron AFTER releasing lock to avoid deadlock
	if shouldStart {
		r.cron.Start()
	}

	r.logf("模型 %s 的调度已启动 (Cron: %s)", modelID, model.ScheduleCron)
	return nil
}

// StopModelSchedule stops the cron schedule for a specific model
func (r *ModelCheckRunner) StopModelSchedule(modelID string) {
	r.mu.Lock()

	entryID, exists := r.cronEntries[modelID]
	if !exists {
		r.mu.Unlock()
		return
	}

	r.cron.Remove(entryID)
	delete(r.cronEntries, modelID)

	shouldStop := len(r.cronEntries) == 0

	r.mu.Unlock()

	// Stop cron AFTER releasing lock to avoid deadlock
	if shouldStop {
		r.cron.Stop()
	}

	r.logf("模型 %s 的调度已停止", modelID)
}

// CheckSingleModel performs a check for a single model
func (r *ModelCheckRunner) CheckSingleModel(modelID string) {
	r.checkSingleModel(modelID, true)
}

// CheckSingleModelNow performs an immediate manual check for a single model
func (r *ModelCheckRunner) CheckSingleModelNow(modelID string) {
	r.checkSingleModel(modelID, false)
}

func (r *ModelCheckRunner) checkSingleModel(modelID string, withRandomDelay bool) {
	r.mu.Lock()
	if _, exists := r.runningModes[modelID]; exists {
		r.mu.Unlock()
		return
	}
	r.runningModes[modelID] = struct{}{}
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.runningModes, modelID)
		r.mu.Unlock()
	}()

	if withRandomDelay {
		// Random delay between 1-10 seconds to avoid fixed intervals
		delay := time.Duration(1+rand.Intn(10)) * time.Second
		time.Sleep(delay)
	}

	// Record check start time
	checkStartTime := time.Now().UTC()

	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Load global config
	globalCfg, err := r.app.loadModelCheckerConfig(ctx)
	if err != nil {
		r.logf("加载全局配置失败: %v", err)
		return
	}

	// Load model configuration
	var model trackedModel
	var lastStatus sql.NullString
	var enabledInt int
	row := r.app.db.QueryRowContext(ctx, `
		SELECT model_id, provider, enabled, schedule_cron, last_status
		FROM model_checker_tracked_models
		WHERE model_id = ?
	`, modelID)

	err = row.Scan(&model.ModelID, &model.Provider, &enabledInt,
		&model.ScheduleCron, &lastStatus)
	if err != nil {
		r.logf("加载模型配置失败: %s - %v", modelID, err)
		return
	}

	model.Enabled = enabledInt == 1
	if lastStatus.Valid {
		model.LastStatus = &lastStatus.String
	}

	if !model.Enabled {
		r.logf("模型 %s 未启用，跳过检查", modelID)
		return
	}

	// Perform check and collect log info
	var statusCode int
	var content string
	var question string
	var finalStatus string

	result := r.checkSingleModelWithLog(ctx, model, globalCfg, &statusCode, &content, &question)
	finalStatus = result.Status

	// Update model status with check start time
	r.updateModelStatusWithCheckTime(ctx, result, checkStartTime)

	// Log everything in one line
	statusText := finalStatus
	if finalStatus == "available" {
		statusText = "正常"
	} else if finalStatus == "unavailable" {
		statusText = "异常"
	} else {
		statusText = "错误"
	}

	timestamp := checkStartTime.Format("2006-01-02 15:04:05")
	if content != "" {
		r.logf("[%s] 开始巡检模型: %s, 巡检问题: %s, 响应状态: %d, 回复内容: %s, 完成巡检, 状态: %s",
			timestamp, modelID, question, statusCode, content, statusText)
	} else {
		r.logf("[%s] 开始巡检模型: %s, 巡检问题: %s, 响应状态: %d, 完成巡检, 状态: %s",
			timestamp, modelID, question, statusCode, statusText)
	}
}



// runModelCheck performs the main check logic for all enabled models






func (r *ModelCheckRunner) checkSingleModelWithLog(ctx context.Context, model trackedModel, globalCfg ModelCheckerConfig, statusCode *int, content *string, question *string) checkModelResult {
	result := checkModelResult{
		ModelID:    model.ModelID,
		Provider:   model.Provider,
		Status:     "error",
		ChangeType: "no_change",
	}

	// Check if test API key is configured
	if globalCfg.TestAPIKey == "" {
		result.ErrorMessage = "测试 API Key 未配置"
		result.Status = "error"
		return result
	}

	// Get app config
	cfg, err := r.app.loadConfig(ctx)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("加载配置失败: %v", err)
		return result
	}

	// Check model availability with test key
	timeout := time.Duration(globalCfg.TimeoutSeconds) * time.Second

	available := r.checkModelWithTestKeyWithLog(ctx, cfg, globalCfg.TestAPIKey, model.ModelID, timeout, statusCode, content, question)

	if available {
		result.Status = "available"
	} else {
		result.Status = "unavailable"
	}

	lastStatus := ""
	if model.LastStatus != nil {
		lastStatus = *model.LastStatus
	}
	result.ChangeType = r.detectChange(lastStatus, result.Status)

	return result
}

func (r *ModelCheckRunner) checkModelWithTestKeyWithLog(ctx context.Context, cfg AppConfig, testKey, modelID string, timeout time.Duration, statusCode *int, content *string, question *string) bool {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+testKey)
	headers.Set("Content-Type", "application/json")

	client := httpClient(timeout)

	// Get global config to access test questions
	globalCfg, err := r.app.loadModelCheckerConfig(ctx)
	if err != nil {
		*statusCode = 0
		return false
	}

	// Pick a random question from the configured questions
	defaultQuestion := "你想一个数字，然后乘以3再减去3，直接给我结果"
	*question = defaultQuestion // default question
	if len(globalCfg.TestQuestions) > 0 {
		*question = globalCfg.TestQuestions[rand.Intn(len(globalCfg.TestQuestions))]
	}

	payload := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": *question},
		},
		"stream":     false,
		"max_tokens": 10,
	}

	response, body, err := doJSON(ctx, client, http.MethodPost, makeURL(cfg.ModelRequestURL, "/v1/chat/completions", nil), headers, payload)
	if err != nil {
		*statusCode = 0
		return false
	}

	*statusCode = response.StatusCode

	// Extract content field
	var respData map[string]any
	if err := json.Unmarshal(body, &respData); err == nil {
		if choices, ok := respData["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if message, ok := choice["message"].(map[string]any); ok {
					if c, ok := message["content"].(string); ok {
						*content = c
					}
				}
			}
		}
	}

	// 2xx status code means model is available
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return true
	}

	// 429 (rate limit) means unavailable due to throttling
	// 502/503/504 (gateway errors) means error
	return false
}

func (r *ModelCheckRunner) detectChange(lastStatus, currentStatus string) string {
	if lastStatus == "" {
		if currentStatus == "available" {
			return "newly_available"
		}
		return "no_change"
	}

	if lastStatus != currentStatus {
		if currentStatus == "available" {
			return "newly_available"
		}
		if currentStatus == "unavailable" {
			return "newly_unavailable"
		}
	}

	return "no_change"
}

func (r *ModelCheckRunner) updateModelStatusWithCheckTime(ctx context.Context, result checkModelResult, checkStartTime time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339)
	checkTime := checkStartTime.Format(time.RFC3339)

	var lastAvailableAt *string
	if result.Status == "available" {
		lastAvailableAt = &now
	}

	_, err := r.app.db.ExecContext(ctx, `
		UPDATE model_checker_tracked_models
		SET last_status = ?,
		    last_checked_at = ?,
		    last_available_at = COALESCE(?, last_available_at),
		    updated_at = datetime('now')
		WHERE model_id = ?
	`, result.Status, checkTime, lastAvailableAt, result.ModelID)

	return err
}
