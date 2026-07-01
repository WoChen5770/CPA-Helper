package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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
	daemonStop     chan struct{}
	daemonDone     chan struct{}
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
	ScheduleCron    string `json:"schedule_cron"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	MaxRetries      int    `json:"max_retries"`
	Enabled         bool   `json:"enabled"`
	AutoStartDaemon bool   `json:"auto_start_daemon"`
}

type modelCheckerSettingsUpdateRequest struct {
	ScheduleCron    *string `json:"schedule_cron"`
	TimeoutSeconds  *int    `json:"timeout_seconds"`
	MaxRetries      *int    `json:"max_retries"`
	Enabled         *bool   `json:"enabled"`
	AutoStartDaemon *bool   `json:"auto_start_daemon"`
}

type trackedModel struct {
	ModelID              string   `json:"model_id"`
	Provider             string   `json:"provider"`
	Enabled              bool     `json:"enabled"`
	CheckIntervalMinutes int      `json:"check_interval_minutes"`
	TimeoutSeconds       int      `json:"timeout_seconds"`
	MaxRetries           int      `json:"max_retries"`
	AlertOnUnavailable   bool     `json:"alert_on_unavailable"`
	LastStatus           string   `json:"last_status"`
	LastAvailableKeys    []string `json:"last_available_keys"`
	LastCheckedAt        *string  `json:"last_checked_at"`
	LastAvailableAt      *string  `json:"last_available_at"`
	FirstSeenAt          *string  `json:"first_seen_at"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
}

type addTrackedModelRequest struct {
	ModelID              string `json:"model_id"`
	Provider             string `json:"provider"`
	CheckIntervalMinutes *int   `json:"check_interval_minutes"`
	TimeoutSeconds       *int   `json:"timeout_seconds"`
	MaxRetries           *int   `json:"max_retries"`
	AlertOnUnavailable   *bool  `json:"alert_on_unavailable"`
}

type updateTrackedModelRequest struct {
	Enabled              *bool `json:"enabled"`
	CheckIntervalMinutes *int  `json:"check_interval_minutes"`
	TimeoutSeconds       *int  `json:"timeout_seconds"`
	MaxRetries           *int  `json:"max_retries"`
	AlertOnUnavailable   *bool `json:"alert_on_unavailable"`
}

type checkModelResult struct {
	ModelID       string
	Provider      string
	Status        string
	AvailableKeys []string
	ErrorMessage  string
	ChangeType    string
}

func newModelCheckRunner(app *App) *ModelCheckRunner {
	return &ModelCheckRunner{
		app:          app,
		runningModes: make(map[string]struct{}),
		state:        "idle",
		logs:         make([]string, 0, modelCheckerMaxInMemoryLogs),
	}
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
		DaemonRunning:  r.daemonStop != nil,
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
	if cfg.ScheduleCron == "" {
		cfg.ScheduleCron = "0 */6 * * *"
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 30
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
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
	case len(parts) == 1 && parts[0] == "run-once":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		go a.modelCheckRunner.RunOnce()
		writeJSON(w, http.StatusOK, map[string]string{"message": "检查已启动"})
		return nil
	case len(parts) == 1 && parts[0] == "start":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		if err := a.modelCheckRunner.StartDaemon(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "Daemon 已启动"})
		return nil
	case len(parts) == 1 && parts[0] == "stop":
		if err := requireMethod(r, http.MethodPost); err != nil {
			return err
		}
		a.modelCheckRunner.Stop()
		writeJSON(w, http.StatusOK, map[string]string{"message": "Daemon 已停止"})
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
			return a.checkTrackedModelNow(w, r, strings.TrimSuffix(modelID, "/check"))
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

	if payload.ScheduleCron != nil {
		_, normalized, err := nextRunTimes(*payload.ScheduleCron, 5, time.Now())
		if err != nil {
			return err
		}
		cfg.ScheduleCron = normalized
	}
	if payload.TimeoutSeconds != nil {
		if *payload.TimeoutSeconds < 1 {
			return validationError("timeout_seconds 不能小于 1")
		}
		cfg.TimeoutSeconds = *payload.TimeoutSeconds
	}
	if payload.MaxRetries != nil {
		if *payload.MaxRetries < 0 || *payload.MaxRetries > 10 {
			return validationError("max_retries 超出范围")
		}
		cfg.MaxRetries = *payload.MaxRetries
	}
	if payload.Enabled != nil {
		cfg.Enabled = *payload.Enabled
	}
	if payload.AutoStartDaemon != nil {
		cfg.AutoStartDaemon = *payload.AutoStartDaemon
	}

	if err := a.saveModelCheckerConfig(r.Context(), cfg); err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, cfg)
	return nil
}

func (a *App) getTrackedModels(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT model_id, provider, enabled, check_interval_minutes, timeout_seconds,
		       max_retries, alert_on_unavailable, last_status, last_available_keys,
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
		var keysJSON sql.NullString
		var enabledInt, alertInt int
		if err := rows.Scan(
			&m.ModelID, &m.Provider, &enabledInt, &m.CheckIntervalMinutes,
			&m.TimeoutSeconds, &m.MaxRetries, &alertInt, &m.LastStatus,
			&keysJSON, &m.LastCheckedAt, &m.LastAvailableAt, &m.FirstSeenAt,
			&m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return err
		}
		m.Enabled = enabledInt == 1
		m.AlertOnUnavailable = alertInt == 1
		if keysJSON.Valid {
			json.Unmarshal([]byte(keysJSON.String), &m.LastAvailableKeys)
		}
		if m.LastAvailableKeys == nil {
			m.LastAvailableKeys = []string{}
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
	checkInterval := 60
	if payload.CheckIntervalMinutes != nil {
		checkInterval = *payload.CheckIntervalMinutes
	}
	timeout := 30
	if payload.TimeoutSeconds != nil {
		timeout = *payload.TimeoutSeconds
	}
	maxRetries := 2
	if payload.MaxRetries != nil {
		maxRetries = *payload.MaxRetries
	}
	alert := 1
	if payload.AlertOnUnavailable != nil && !*payload.AlertOnUnavailable {
		alert = 0
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.db.ExecContext(r.Context(), `
		INSERT INTO model_checker_tracked_models
		(model_id, provider, enabled, check_interval_minutes, timeout_seconds,
		 max_retries, alert_on_unavailable, first_seen_at, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
	`, payload.ModelID, payload.Provider, checkInterval, timeout, maxRetries, alert, now, now, now)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "模型已添加到监控"})
	return nil
}

func (a *App) getTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	var m trackedModel
	var keysJSON sql.NullString
	var enabledInt, alertInt int
	err := a.db.QueryRowContext(r.Context(), `
		SELECT model_id, provider, enabled, check_interval_minutes, timeout_seconds,
		       max_retries, alert_on_unavailable, last_status, last_available_keys,
		       last_checked_at, last_available_at, first_seen_at, created_at, updated_at
		FROM model_checker_tracked_models
		WHERE model_id = ?
	`, modelID).Scan(
		&m.ModelID, &m.Provider, &enabledInt, &m.CheckIntervalMinutes,
		&m.TimeoutSeconds, &m.MaxRetries, &alertInt, &m.LastStatus,
		&keysJSON, &m.LastCheckedAt, &m.LastAvailableAt, &m.FirstSeenAt,
		&m.CreatedAt, &m.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return notFoundError("模型未找到")
	}
	if err != nil {
		return err
	}

	m.Enabled = enabledInt == 1
	m.AlertOnUnavailable = alertInt == 1
	if keysJSON.Valid {
		json.Unmarshal([]byte(keysJSON.String), &m.LastAvailableKeys)
	}
	if m.LastAvailableKeys == nil {
		m.LastAvailableKeys = []string{}
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
	if payload.CheckIntervalMinutes != nil {
		if *payload.CheckIntervalMinutes < 1 {
			return validationError("check_interval_minutes 不能小于 1")
		}
		updates = append(updates, "check_interval_minutes = ?")
		args = append(args, *payload.CheckIntervalMinutes)
	}
	if payload.TimeoutSeconds != nil {
		if *payload.TimeoutSeconds < 1 {
			return validationError("timeout_seconds 不能小于 1")
		}
		updates = append(updates, "timeout_seconds = ?")
		args = append(args, *payload.TimeoutSeconds)
	}
	if payload.MaxRetries != nil {
		if *payload.MaxRetries < 0 || *payload.MaxRetries > 10 {
			return validationError("max_retries 超出范围")
		}
		updates = append(updates, "max_retries = ?")
		args = append(args, *payload.MaxRetries)
	}
	if payload.AlertOnUnavailable != nil {
		updates = append(updates, "alert_on_unavailable = ?")
		if *payload.AlertOnUnavailable {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
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

	writeJSON(w, http.StatusOK, map[string]string{"message": "配置已更新"})
	return nil
}

func (a *App) deleteTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	_, err := a.db.ExecContext(r.Context(), `
		DELETE FROM model_checker_tracked_models WHERE model_id = ?
	`, modelID)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "模型已从监控移除"})
	return nil
}

func (a *App) checkTrackedModelNow(w http.ResponseWriter, r *http.Request, modelID string) error {
	go a.modelCheckRunner.CheckSingleModel(modelID)
	writeJSON(w, http.StatusOK, map[string]string{"message": "检查已启动"})
	return nil
}

// Core checking logic - RunOnce performs a single check run for all enabled models
func (r *ModelCheckRunner) RunOnce() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	mode := "once"
	r.runningModes[mode] = struct{}{}
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		delete(r.runningModes, mode)
		r.mu.Unlock()
	}()

	r.runModelCheck(mode)
}

func (r *ModelCheckRunner) CheckSingleModel(modelID string) {
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

	r.logf("开始检查单个模型: %s", modelID)

	ctx := context.Background()

	// Load model configuration
	var model trackedModel
	var lastAvailableKeysJSON string
	row := r.app.db.QueryRowContext(ctx, `
		SELECT model_id, provider, enabled, timeout_seconds, max_retries,
		       last_status, last_available_keys
		FROM model_checker_tracked_models
		WHERE model_id = ?
	`, modelID)

	err := row.Scan(&model.ModelID, &model.Provider, &model.Enabled,
		&model.TimeoutSeconds, &model.MaxRetries,
		&model.LastStatus, &lastAvailableKeysJSON)
	if err != nil {
		r.logf("加载模型配置失败: %s - %v", modelID, err)
		return
	}

	if lastAvailableKeysJSON != "" && lastAvailableKeysJSON != "null" {
		json.Unmarshal([]byte(lastAvailableKeysJSON), &model.LastAvailableKeys)
	}

	// Perform check
	result := r.checkSingleModel(ctx, model)

	// Update model status
	r.updateModelStatus(ctx, result)

	r.logf("完成检查模型: %s - 状态: %s", modelID, result.Status)
}

func (r *ModelCheckRunner) StartDaemon() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.daemonStop != nil {
		return fmt.Errorf("Daemon 已在运行")
	}

	// Load configuration
	ctx := context.Background()
	cfg, err := r.app.loadModelCheckerConfig(ctx)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	if !cfg.Enabled {
		return fmt.Errorf("模型巡检未启用")
	}

	// Parse cron schedule
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(cfg.ScheduleCron)
	if err != nil {
		return fmt.Errorf("Cron 表达式无效: %w", err)
	}

	// Set timezone
	if spec, ok := schedule.(*cron.SpecSchedule); ok {
		spec.Location = appTimeLocation
	}

	r.daemonStop = make(chan struct{})
	r.daemonDone = make(chan struct{})
	r.cron = cron.New(cron.WithLocation(appTimeLocation))

	// Add cron job
	_, err = r.cron.AddFunc(cfg.ScheduleCron, func() {
		r.logf("定时触发模型巡检")
		go r.runModelCheck("daemon")
	})
	if err != nil {
		r.daemonStop = nil
		r.daemonDone = nil
		r.cron = nil
		return fmt.Errorf("添加定时任务失败: %w", err)
	}

	r.logf("Daemon 已启动 - Cron: %s", cfg.ScheduleCron)
	r.cron.Start()

	// Monitor stop signal
	go func() {
		<-r.daemonStop
		if r.cron != nil {
			r.cron.Stop()
		}
		close(r.daemonDone)
	}()

	return nil
}

func (r *ModelCheckRunner) Stop() {
	r.mu.Lock()
	stopChan := r.daemonStop
	doneChan := r.daemonDone
	r.mu.Unlock()

	if stopChan == nil {
		return
	}

	r.logf("停止 Daemon...")
	close(stopChan)
	if doneChan != nil {
		<-doneChan
	}

	r.mu.Lock()
	r.daemonStop = nil
	r.daemonDone = nil
	if r.cron != nil {
		r.cron.Stop()
		r.cron = nil
	}
	r.mu.Unlock()
}

// runModelCheck performs the main check logic for all enabled models
func (r *ModelCheckRunner) runModelCheck(mode string) {
	ctx := context.Background()

	r.mu.Lock()
	r.state = "running"
	r.detail = ""
	modeStr := mode
	r.mode = &modeStr
	now := time.Now()
	r.lastStartedAt = &now
	r.stats = modelCheckStats{}
	r.mu.Unlock()

	r.logf("开始模型巡检 (模式: %s)", mode)

	// Create run record
	runID, err := r.createRunRecord(ctx, mode)
	if err != nil {
		r.finishRun("failed", fmt.Sprintf("创建运行记录失败: %v", err))
		return
	}

	// Load enabled models
	models, err := r.loadEnabledModels(ctx)
	if err != nil {
		r.updateRunRecord(ctx, runID, "failed", fmt.Sprintf("加载模型列表失败: %v", err))
		r.finishRun("failed", fmt.Sprintf("加载模型列表失败: %v", err))
		return
	}

	if len(models) == 0 {
		r.logf("没有启用的监控模型")
		r.updateRunRecord(ctx, runID, "completed", "没有启用的监控模型")
		r.finishRun("completed", "没有启用的监控模型")
		return
	}

	r.logf("找到 %d 个启用的监控模型", len(models))

	// Check each model
	results := make([]checkModelResult, 0, len(models))
	for _, model := range models {
		r.logf("检查模型: %s", model.ModelID)
		result := r.checkSingleModel(ctx, model)
		results = append(results, result)

		// Update statistics
		r.mu.Lock()
		r.stats.TotalModels++
		switch result.Status {
		case "available":
			r.stats.AvailableModels++
		case "unavailable":
			r.stats.UnavailableModels++
		case "error":
			r.stats.ErrorModels++
		}
		switch result.ChangeType {
		case "newly_available":
			r.stats.NewlyAvailable++
		case "newly_unavailable":
			r.stats.NewlyUnavailable++
		}
		r.mu.Unlock()
	}

	// Save run details
	if err := r.saveRunDetails(ctx, runID, results); err != nil {
		r.logf("保存巡检详情失败: %v", err)
	}

	// Update model statuses
	for _, result := range results {
		r.updateModelStatus(ctx, result)
	}

	// Update run record
	r.mu.Lock()
	stats := r.stats
	r.mu.Unlock()

	r.updateRunRecordWithStats(ctx, runID, "completed", "", stats)
	r.finishRun("completed", fmt.Sprintf("检查完成: %d 个模型", stats.TotalModels))
}

func (r *ModelCheckRunner) createRunRecord(ctx context.Context, mode string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := r.app.db.ExecContext(ctx, `
		INSERT INTO model_checker_runs (mode, state, started_at)
		VALUES (?, 'running', ?)
	`, mode, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *ModelCheckRunner) updateRunRecord(ctx context.Context, runID int64, state, detail string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.app.db.ExecContext(ctx, `
		UPDATE model_checker_runs
		SET state = ?, detail = ?, finished_at = ?, updated_at = datetime('now')
		WHERE id = ?
	`, state, detail, now, runID)
	return err
}

func (r *ModelCheckRunner) updateRunRecordWithStats(ctx context.Context, runID int64, state, detail string, stats modelCheckStats) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.app.db.ExecContext(ctx, `
		UPDATE model_checker_runs
		SET state = ?, detail = ?, finished_at = ?,
		    total_models = ?, available_models = ?, unavailable_models = ?,
		    newly_available = ?, newly_unavailable = ?, error_models = ?,
		    updated_at = datetime('now')
		WHERE id = ?
	`, state, detail, now,
		stats.TotalModels, stats.AvailableModels, stats.UnavailableModels,
		stats.NewlyAvailable, stats.NewlyUnavailable, stats.ErrorModels,
		runID)
	return err
}

func (r *ModelCheckRunner) finishRun(state, detail string) {
	r.mu.Lock()
	r.state = state
	r.detail = detail
	now := time.Now()
	r.lastFinishedAt = &now
	r.mu.Unlock()
	r.logf("巡检完成 - 状态: %s", state)
}

func (r *ModelCheckRunner) loadEnabledModels(ctx context.Context) ([]trackedModel, error) {
	rows, err := r.app.db.QueryContext(ctx, `
		SELECT model_id, provider, timeout_seconds, max_retries,
		       last_status, last_available_keys
		FROM model_checker_tracked_models
		WHERE enabled = 1
		ORDER BY model_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	models := []trackedModel{}
	for rows.Next() {
		var m trackedModel
		var lastAvailableKeysJSON string
		err := rows.Scan(&m.ModelID, &m.Provider, &m.TimeoutSeconds,
			&m.MaxRetries, &m.LastStatus, &lastAvailableKeysJSON)
		if err != nil {
			return nil, err
		}
		if lastAvailableKeysJSON != "" && lastAvailableKeysJSON != "null" {
			json.Unmarshal([]byte(lastAvailableKeysJSON), &m.LastAvailableKeys)
		}
		models = append(models, m)
	}
	return models, rows.Err()
}

func (r *ModelCheckRunner) checkSingleModel(ctx context.Context, model trackedModel) checkModelResult {
	result := checkModelResult{
		ModelID:       model.ModelID,
		Provider:      model.Provider,
		Status:        "error",
		AvailableKeys: []string{},
		ChangeType:    "no_change",
	}

	// Get app config
	cfg, err := r.app.loadConfig(ctx)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("加载配置失败: %v", err)
		return result
	}

	// Get all API keys
	keys, err := r.loadAPIKeys(ctx)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("加载 API Keys 失败: %v", err)
		return result
	}

	if len(keys) == 0 {
		result.ErrorMessage = "没有可用的 API Keys"
		result.Status = "unavailable"
		result.ChangeType = r.detectChange(model.LastStatus, result.Status, model.LastAvailableKeys, result.AvailableKeys)
		return result
	}

	// Check model availability on each key
	timeout := time.Duration(model.TimeoutSeconds) * time.Second
	availableKeys := []string{}

	for _, key := range keys {
		if r.checkModelOnKey(ctx, cfg, key, model.ModelID, timeout) {
			availableKeys = append(availableKeys, key)
		}
	}

	result.AvailableKeys = availableKeys
	if len(availableKeys) > 0 {
		result.Status = "available"
	} else {
		result.Status = "unavailable"
	}

	result.ChangeType = r.detectChange(model.LastStatus, result.Status, model.LastAvailableKeys, result.AvailableKeys)

	return result
}

func (r *ModelCheckRunner) loadAPIKeys(ctx context.Context) ([]string, error) {
	rows, err := r.app.db.QueryContext(ctx, `
		SELECT api_key FROM api_keys WHERE api_key IS NOT NULL AND api_key != ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *ModelCheckRunner) checkModelOnKey(ctx context.Context, cfg AppConfig, apiKey, modelID string, timeout time.Duration) bool {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+apiKey)

	client := httpClient(timeout)
	url := makeURL(cfg.Collector.CLIProxyURL, "/v1/models", nil)

	response, payload, err := doJSON(ctx, client, http.MethodGet, url, headers, nil)
	if err != nil {
		return false
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}

	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return false
	}

	items, err := extractAvailableModelItems(raw)
	if err != nil {
		return false
	}

	// Check if model exists in the list
	for _, item := range items {
		if text, ok := item.(string); ok {
			if strings.TrimSpace(text) == modelID {
				return true
			}
		} else if obj, ok := item.(map[string]any); ok {
			if id := firstStringValue(obj, modelIDKeys); id != nil && *id == modelID {
				return true
			}
		}
	}

	return false
}

func (r *ModelCheckRunner) detectChange(lastStatus, currentStatus string, lastKeys, currentKeys []string) string {
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

	// Check if keys changed
	if currentStatus == "available" && !stringSlicesEqual(lastKeys, currentKeys) {
		return "keys_changed"
	}

	return "no_change"
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := make(map[string]bool)
	for _, s := range a {
		aMap[s] = true
	}
	for _, s := range b {
		if !aMap[s] {
			return false
		}
	}
	return true
}

func (r *ModelCheckRunner) saveRunDetails(ctx context.Context, runID int64, results []checkModelResult) error {
	if len(results) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	for _, result := range results {
		availableKeysJSON, _ := json.Marshal(result.AvailableKeys)
		_, err := r.app.db.ExecContext(ctx, `
			INSERT INTO model_checker_run_models
			(run_id, model_id, provider, status, available_keys, error_message, change_type, checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, runID, result.ModelID, result.Provider, result.Status,
			string(availableKeysJSON), result.ErrorMessage, result.ChangeType, now)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *ModelCheckRunner) updateModelStatus(ctx context.Context, result checkModelResult) error {
	now := time.Now().UTC().Format(time.RFC3339)
	availableKeysJSON, _ := json.Marshal(result.AvailableKeys)

	var lastAvailableAt *string
	if result.Status == "available" {
		lastAvailableAt = &now
	}

	_, err := r.app.db.ExecContext(ctx, `
		UPDATE model_checker_tracked_models
		SET last_status = ?,
		    last_available_keys = ?,
		    last_checked_at = ?,
		    last_available_at = COALESCE(?, last_available_at),
		    updated_at = datetime('now')
		WHERE model_id = ?
	`, result.Status, string(availableKeysJSON), now, lastAvailableAt, result.ModelID)

	return err
}
