package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
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
func (r *ModelCheckRunner) status() modelCheckStatusResponse {
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

func (r *ModelCheckRunner) clearLogs() {
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
			return a.getModelCheckerSettings(w, r)
		}
		if r.Method == http.MethodPut {
			return a.updateModelCheckerSettings(w, r)
		}
	case len(parts) == 1 && parts[0] == "status":
		if r.Method == http.MethodGet {
			return a.getModelCheckerStatus(w, r)
		}
	case len(parts) == 1 && parts[0] == "run-once":
		if r.Method == http.MethodPost {
			return a.runModelCheckerOnce(w, r)
		}
	case len(parts) == 1 && parts[0] == "start":
		if r.Method == http.MethodPost {
			return a.startModelCheckerDaemon(w, r)
		}
	case len(parts) == 1 && parts[0] == "stop":
		if r.Method == http.MethodPost {
			return a.stopModelCheckerDaemon(w, r)
		}
	case len(parts) == 2 && parts[0] == "logs" && parts[1] == "clear":
		if r.Method == http.MethodPost {
			return a.clearModelCheckerLogs(w, r)
		}
	case len(parts) == 1 && parts[0] == "models":
		if r.Method == http.MethodGet {
			return a.getTrackedModels(w, r)
		}
		if r.Method == http.MethodPost {
			return a.addTrackedModel(w, r)
		}
	case len(parts) >= 2 && parts[0] == "models":
		modelID := strings.Join(parts[1:], "/")
		if strings.HasSuffix(modelID, "/check") {
			if r.Method == http.MethodPost {
				return a.checkTrackedModelNow(w, r, strings.TrimSuffix(modelID, "/check"))
			}
		} else {
			if r.Method == http.MethodGet {
				return a.getTrackedModel(w, r, modelID)
			}
			if r.Method == http.MethodPut {
				return a.updateTrackedModel(w, r, modelID)
			}
			if r.Method == http.MethodDelete {
				return a.deleteTrackedModel(w, r, modelID)
			}
		}
	}
	return notFoundError("Not Found")
}

func (a *App) getModelCheckerSettings(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.loadModelCheckerConfig(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cfg)
	return nil
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

func (a *App) getModelCheckerStatus(w http.ResponseWriter, r *http.Request) error {
	status := a.modelCheckRunner.status()
	writeJSON(w, http.StatusOK, status)
	return nil
}

func (a *App) runModelCheckerOnce(w http.ResponseWriter, r *http.Request) error {
	go a.modelCheckRunner.runOnce()
	writeJSON(w, http.StatusOK, map[string]any{"message": "检查已启动"})
	return nil
}

func (a *App) startModelCheckerDaemon(w http.ResponseWriter, r *http.Request) error {
	if err := a.modelCheckRunner.startDaemon(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Daemon 已启动"})
	return nil
}

func (a *App) stopModelCheckerDaemon(w http.ResponseWriter, r *http.Request) error {
	a.modelCheckRunner.stopDaemon()
	writeJSON(w, http.StatusOK, map[string]any{"message": "Daemon 已停止"})
	return nil
}

func (a *App) clearModelCheckerLogs(w http.ResponseWriter, r *http.Request) error {
	a.modelCheckRunner.clearLogs()
	writeJSON(w, http.StatusOK, map[string]any{"message": "日志已清除"})
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

	writeJSON(w, http.StatusOK, map[string]any{"message": "模型已添加到监控"})
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

	writeJSON(w, http.StatusOK, map[string]any{"message": "配置已更新"})
	return nil
}

func (a *App) deleteTrackedModel(w http.ResponseWriter, r *http.Request, modelID string) error {
	_, err := a.db.ExecContext(r.Context(), `
		DELETE FROM model_checker_tracked_models WHERE model_id = ?
	`, modelID)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusOK, map[string]any{"message": "模型已从监控移除"})
	return nil
}

func (a *App) checkTrackedModelNow(w http.ResponseWriter, r *http.Request, modelID string) error {
	go a.modelCheckRunner.checkSingleModel(modelID)
	writeJSON(w, http.StatusOK, map[string]any{"message": "检查已启动"})
	return nil
}

// Core checking logic - simplified stub for now
func (r *ModelCheckRunner) runOnce() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()

	r.logf("模型巡检功能开发中...")

	r.mu.Lock()
	r.running = false
	r.mu.Unlock()
}

func (r *ModelCheckRunner) checkSingleModel(modelID string) {
	r.logf("单模型检查功能开发中: %s", modelID)
}

func (r *ModelCheckRunner) startDaemon() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.daemonStop != nil {
		return fmt.Errorf("Daemon 已在运行")
	}
	r.logf("Daemon 功能开发中...")
	return nil
}

func (r *ModelCheckRunner) stopDaemon() {
	r.logf("停止 Daemon...")
}
