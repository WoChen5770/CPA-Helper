package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	slashpath "path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"cpa-helper/backend/internal/app/web"
	"cpa-helper/backend/internal/platform/cpahttp"
	"cpa-helper/backend/internal/security"
	backendMigrations "cpa-helper/backend/migrations"
	"github.com/pressly/goose/v3"
	"github.com/robfig/cron/v3"
	_ "modernc.org/sqlite"
)

const (
	defaultCPAURL = "http://127.0.0.1:8317"
)

var appTimeLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var defaultKeeperPriorityRules = map[string]int{
	"pro_20x": 20,
	"pro_5x":  5,
	"plus":    4,
	"team":    3,
	"free":    0,
}

type App struct {
	db               *sql.DB
	repoRoot         string
	dataDir          string
	frontendDist     string
	frontendFS       fs.FS
	frontendEnv      bool
	collector        *CollectorRunner
	keeper           *KeeperRunner
	keeperUsageCache keeperWindowUsageCache
	modelCheckRunner *ModelCheckRunner
}

type AppError struct {
	Code    string
	Message string
	Status  int
}

func (e *AppError) Error() string {
	return e.Message
}

func appError(code string, status int, message string) *AppError {
	return &AppError{Code: code, Status: status, Message: message}
}

func authenticationError(message string) *AppError {
	return appError("authentication_failed", http.StatusUnauthorized, message)
}

func forbiddenError(message string) *AppError {
	return appError("forbidden", http.StatusForbidden, message)
}

func notFoundError(message string) *AppError {
	return appError("not_found", http.StatusNotFound, message)
}

func conflictError(message string) *AppError {
	return appError("conflict", http.StatusConflict, message)
}

func validationError(message string) *AppError {
	return appError("validation_error", http.StatusUnprocessableEntity, message)
}

func New() (*App, error) {
	return NewWithOptions(context.Background(), NewOptions{
		Migrate:         true,
		StartBackground: true,
	})
}

func NewWithOptions(ctx context.Context, options NewOptions) (*App, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	paths, err := resolveRuntimePaths()
	if err != nil {
		return nil, err
	}
	if options.RequireReady && !options.Migrate {
		if _, err := checkStartupPaths(ctx, paths); err != nil {
			return nil, err
		}
	}
	db, err := openRuntimeDB(paths, false)
	if err != nil {
		return nil, err
	}

	frontendDist, frontendEnv := frontendDistDir(paths.RepoRoot)
	frontendFS, _ := web.DistFS()
	app := &App{
		db:           db,
		repoRoot:     paths.RepoRoot,
		dataDir:      paths.DataDir,
		frontendDist: frontendDist,
		frontendFS:   frontendFS,
		frontendEnv:  frontendEnv,
	}
	if options.Migrate {
		if err := app.runMigrations(ctx); err != nil {
			db.Close()
			return nil, err
		}
	}
	if options.RequireReady {
		if _, err := checkDatabaseReady(ctx, db, paths.DBPath); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := app.loadConfig(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if options.StartBackground {
		app.startBackground(ctx)
	}
	return app, nil
}

func (a *App) startBackground(ctx context.Context) {
	a.collector = NewCollectorRunner(a)
	a.keeper = NewKeeperRunner(a)
	a.modelCheckRunner = newModelCheckRunner(a)
	a.collector.Start()
	a.keeper.LoadPersistedState(ctx)
	a.keeper.StartAutoIfConfigured()
}

func (a *App) Close() {
	if a.collector != nil {
		a.collector.Stop()
	}
	if a.keeper != nil {
		a.keeper.Stop()
	}
	if a.modelCheckRunner != nil {
		// Stop all cron schedules
		if a.modelCheckRunner.cron != nil {
			a.modelCheckRunner.cron.Stop()
		}
	}
	if a.db != nil {
		a.db.Close()
	}
}

func detectRepoRoot() (string, error) {
	if root := os.Getenv("CPA_HELPER_REPO_ROOT"); strings.TrimSpace(root) != "" {
		return filepath.Abs(root)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	executablePath, _ := os.Executable()
	return detectRepoRootFrom(cwd, executablePath)
}

func detectRepoRootFrom(cwd, executablePath string) (string, error) {
	if root, ok := findProjectRoot(cwd); ok {
		return root, nil
	}
	if executablePath != "" {
		executableDir := filepath.Dir(executablePath)
		if root, ok := findProjectRoot(executableDir); ok {
			return root, nil
		}
		return filepath.Abs(executableDir)
	}
	return filepath.Abs(cwd)
}

func findProjectRoot(start string) (string, bool) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if isProjectRoot(current) {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func isProjectRoot(path string) bool {
	if _, err := os.Stat(filepath.Join(path, "frontend")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, "backend")); err != nil {
		return false
	}
	return true
}

func frontendDistDir(repoRoot string) (string, bool) {
	if value := strings.TrimSpace(os.Getenv("CPA_HELPER_FRONTEND_DIST")); value != "" {
		return value, true
	}
	return filepath.Join(repoRoot, "frontend", "dist"), false
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// 健康检查接口 - 支持任意 base path
	healthHandler := a.wrap(func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return nil
	})
	mux.HandleFunc("GET /api/health", healthHandler)
	mux.HandleFunc("GET /{basePath}/api/health", healthHandler)

	readyHandler := a.wrap(func(w http.ResponseWriter, r *http.Request) error {
		report, err := a.Readiness(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "not_ready",
				"detail": map[string]string{
					"code":    "startup_check_failed",
					"message": err.Error(),
				},
			})
			return nil
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":          "ready",
			"current_version": report.CurrentVersion,
			"target_version":  report.TargetVersion,
		})
		return nil
	})
	mux.HandleFunc("GET /api/ready", readyHandler)
	mux.HandleFunc("GET /{basePath}/api/ready", readyHandler)

	// API 路由 - 支持任意 base path（/api/* 或 /xxx/api/*）
	mux.HandleFunc("/api/auth/", a.wrap(a.handleAuth))
	mux.HandleFunc("/{basePath}/api/auth/", a.wrap(a.handleAuth))

	mux.HandleFunc("/api/settings", a.wrap(a.handleSettings))
	mux.HandleFunc("/{basePath}/api/settings", a.wrap(a.handleSettings))

	mux.HandleFunc("/api/collector/status", a.wrap(a.handleCollectorStatus))
	mux.HandleFunc("/{basePath}/api/collector/status", a.wrap(a.handleCollectorStatus))

	mux.HandleFunc("/api/usage/", a.wrap(a.handleUsage))
	mux.HandleFunc("/{basePath}/api/usage/", a.wrap(a.handleUsage))

	mux.HandleFunc("/api/model-prices", a.wrap(a.handleModelPrices))
	mux.HandleFunc("/{basePath}/api/model-prices", a.wrap(a.handleModelPrices))

	mux.HandleFunc("/api/model-prices/", a.wrap(a.handleModelPriceByPath))
	mux.HandleFunc("/{basePath}/api/model-prices/", a.wrap(a.handleModelPriceByPath))

	mux.HandleFunc("/api/users", a.wrap(a.handleUsers))
	mux.HandleFunc("/{basePath}/api/users", a.wrap(a.handleUsers))

	mux.HandleFunc("/api/users/", a.wrap(a.handleUserByPath))
	mux.HandleFunc("/{basePath}/api/users/", a.wrap(a.handleUserByPath))

	mux.HandleFunc("/api/account/quota", a.wrap(a.handleCurrentUserQuota))
	mux.HandleFunc("/{basePath}/api/account/quota", a.wrap(a.handleCurrentUserQuota))

	mux.HandleFunc("/api/api-keys", a.wrap(a.handleCurrentUserAPIKeys))
	mux.HandleFunc("/{basePath}/api/api-keys", a.wrap(a.handleCurrentUserAPIKeys))

	mux.HandleFunc("/api/api-keys/", a.wrap(a.handleCurrentUserAPIKeyByHash))
	mux.HandleFunc("/{basePath}/api/api-keys/", a.wrap(a.handleCurrentUserAPIKeyByHash))

	mux.HandleFunc("/api/account/models", a.wrap(a.handleAvailableModels))
	mux.HandleFunc("/{basePath}/api/account/models", a.wrap(a.handleAvailableModels))

	mux.HandleFunc("/api/account/model-request/test", a.wrap(a.handleCurrentModelRequestTest))
	mux.HandleFunc("/{basePath}/api/account/model-request/test", a.wrap(a.handleCurrentModelRequestTest))

	mux.HandleFunc("/api/account/model-request", a.wrap(a.handleCurrentModelRequestGuide))
	mux.HandleFunc("/{basePath}/api/account/model-request", a.wrap(a.handleCurrentModelRequestGuide))

	mux.HandleFunc("/api/card-shops/favorites", a.wrap(a.handleCardShopFavorites))
	mux.HandleFunc("/{basePath}/api/card-shops/favorites", a.wrap(a.handleCardShopFavorites))

	mux.HandleFunc("/api/card-shops/tags", a.wrap(a.handleCardShopTags))
	mux.HandleFunc("/{basePath}/api/card-shops/tags", a.wrap(a.handleCardShopTags))

	mux.HandleFunc("/api/card-shops", a.wrap(a.handleCardShops))
	mux.HandleFunc("/{basePath}/api/card-shops", a.wrap(a.handleCardShops))

	mux.HandleFunc("/api/codex-keeper/", a.wrap(a.handleCodexKeeper))
	mux.HandleFunc("/{basePath}/api/codex-keeper/", a.wrap(a.handleCodexKeeper))

	mux.HandleFunc("/api/model-checker/", a.wrap(a.handleModelChecker))
	mux.HandleFunc("/{basePath}/api/model-checker/", a.wrap(a.handleModelChecker))

	// SPA 和静态资源 - 捕获所有其他请求
	mux.HandleFunc("/", a.wrap(a.handleSPA))
	return withCORS(mux)
}

type handlerFunc func(http.ResponseWriter, *http.Request) error

func (a *App) wrap(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic handling %s %s: %v", r.Method, r.URL.Path, recovered)
				writeAppError(w, appError("app_error", http.StatusInternalServerError, "服务器内部错误"))
			}
		}()
		if err := fn(w, r); err != nil {
			var appErr *AppError
			if errors.As(err, &appErr) {
				writeAppError(w, appErr)
				return
			}
			log.Printf("request failed %s %s: %v", r.Method, r.URL.Path, err)
			writeAppError(w, appError("app_error", http.StatusInternalServerError, "服务器内部错误"))
		}
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "http://127.0.0.1:5173" || origin == "http://localhost:5173" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Management-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(apiJSONValue(value))
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func writeAppError(w http.ResponseWriter, err *AppError) {
	if err.Status == 0 {
		err.Status = http.StatusBadRequest
	}
	writeJSON(w, err.Status, map[string]any{
		"detail": map[string]string{
			"code":    err.Code,
			"message": err.Message,
		},
	})
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := decoder.Decode(target); err != nil {
		return validationError("请求体不是有效 JSON")
	}
	return nil
}

func methodNotAllowed() error {
	return appError("method_not_allowed", http.StatusMethodNotAllowed, "Method Not Allowed")
}

func requireMethod(r *http.Request, methods ...string) error {
	for _, method := range methods {
		if r.Method == method {
			return nil
		}
	}
	return methodNotAllowed()
}

func (a *App) handleSPA(w http.ResponseWriter, r *http.Request) error {
	// 检查是否是 API 路径（不应该到达这里，但作为保险）
	if strings.Contains(r.URL.Path, "/api/") {
		return notFoundError("Not Found")
	}

	// 提取 base path（如果有）
	// 例如：/cpa-helper/admin/usage -> basePath = /cpa-helper, requestPath = /admin/usage
	//      /login -> basePath = "", requestPath = /login
	basePath, requestPath := extractBasePath(r.URL.Path)

	// 如果访问的是 basePath 本身（没有尾部斜杠），重定向到带斜杠的版本
	// 例如：/cpa-helper -> /cpa-helper/
	if basePath != "" && requestPath == "" {
		http.Redirect(w, r, basePath+"/", http.StatusMovedPermanently)
		return nil
	}

	if a.frontendEnv {
		served, err := a.serveExternalSPAWithPath(w, r, requestPath, basePath)
		if err != nil || served {
			return err
		}
		return a.serveFrontendNotBuilt(w)
	}
	if a.frontendFS != nil {
		served, err := a.serveEmbeddedSPAWithPath(w, r, requestPath, basePath)
		if err != nil || served {
			return err
		}
	}
	served, err := a.serveExternalSPAWithPath(w, r, requestPath, basePath)
	if err != nil || served {
		return err
	}
	return a.serveFrontendNotBuilt(w)
}

// extractBasePath 从请求路径中提取 base path
// 例如：/cpa-helper/login -> (/cpa-helper, /login)
//
//	/login -> ("", /login)
//	/some-path/admin/usage -> (/some-path, /admin/usage)
func extractBasePath(fullPath string) (basePath string, requestPath string) {
	// 已知的应用路由（这些不是 base path）
	knownRoutes := []string{"/login", "/change-credentials", "/admin", "/account", "/usage", "/records", "/keys", "/pricing", "/settings", "/users"}

	// 检查是否直接匹配已知路由
	for _, route := range knownRoutes {
		if strings.HasPrefix(fullPath, route) {
			return "", fullPath
		}
	}

	// 尝试提取第一段作为 base path
	parts := strings.SplitN(strings.TrimPrefix(fullPath, "/"), "/", 2)
	if len(parts) >= 1 && parts[0] != "" {
		// 检查第二部分是否是已知路由
		if len(parts) == 2 {
			secondPart := "/" + parts[1]
			for _, route := range knownRoutes {
				if strings.HasPrefix(secondPart, route) {
					return "/" + parts[0], secondPart
				}
			}
			// 如果第二部分为空或是 assets 等静态资源目录
			if parts[1] == "" || strings.HasPrefix(parts[1], "assets/") || strings.HasPrefix(parts[1], "logo.png") {
				return "/" + parts[0], "/" + parts[1]
			}
		} else if len(parts) == 1 {
			// 只有一段路径，可能是 /cpa-helper 这种情况
			// 返回这个路径作为 basePath，requestPath 为空
			return "/" + parts[0], ""
		}
	}

	return "", fullPath
}

func (a *App) serveExternalSPA(w http.ResponseWriter, r *http.Request) (bool, error) {
	return a.serveExternalSPAWithPath(w, r, r.URL.Path, "")
}

func (a *App) serveExternalSPAWithPath(w http.ResponseWriter, r *http.Request, requestPath string, basePath string) (bool, error) {
	requested := cleanSPAPath(requestPath)
	if requested != "" {
		staticPath := filepath.Join(a.frontendDist, filepath.FromSlash(requested))
		if insideDir(a.frontendDist, staticPath) {
			if info, err := os.Stat(staticPath); err == nil && !info.IsDir() {
				http.ServeFile(w, r, staticPath)
				return true, nil
			}
		}
	}
	indexPath := filepath.Join(a.frontendDist, "index.html")
	if _, err := os.Stat(indexPath); err == nil {
		return true, a.serveIndexHTMLFileWithBase(w, r, indexPath, basePath)
	}
	return false, nil
}

func (a *App) serveEmbeddedSPA(w http.ResponseWriter, r *http.Request) (bool, error) {
	return a.serveEmbeddedSPAWithPath(w, r, r.URL.Path, "")
}

func (a *App) serveEmbeddedSPAWithPath(w http.ResponseWriter, r *http.Request, requestPath string, basePath string) (bool, error) {
	requested := cleanSPAPath(requestPath)
	if requested != "" && fs.ValidPath(requested) {
		if info, err := fs.Stat(a.frontendFS, requested); err == nil && !info.IsDir() {
			return true, serveFSFile(w, r, a.frontendFS, requested)
		}
	}
	if _, err := fs.Stat(a.frontendFS, "index.html"); err == nil {
		return true, a.serveFSIndexHTMLWithBase(w, r, a.frontendFS, basePath)
	}
	return false, nil
}

func cleanSPAPath(requestPath string) string {
	cleaned := slashpath.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	if cleaned == "/" {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func serveFSFile(w http.ResponseWriter, r *http.Request, filesystem fs.FS, name string) error {
	data, err := fs.ReadFile(filesystem, name)
	if err != nil {
		return err
	}
	info, err := fs.Stat(filesystem, name)
	if err != nil {
		return err
	}
	http.ServeContent(w, r, slashpath.Base(name), info.ModTime(), bytes.NewReader(data))
	return nil
}

func (a *App) serveIndexHTMLFileWithBase(w http.ResponseWriter, r *http.Request, indexPath string, basePath string) error {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	html := string(data)
	if basePath != "" {
		// 在 <head> 标签后注入 <base> 标签
		baseTag := fmt.Sprintf(`<base href="%s/">`, basePath)
		html = strings.Replace(html, "<head>", "<head>\n    "+baseTag, 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
	return nil
}

func (a *App) serveFSIndexHTMLWithBase(w http.ResponseWriter, r *http.Request, filesystem fs.FS, basePath string) error {
	data, err := fs.ReadFile(filesystem, "index.html")
	if err != nil {
		return err
	}
	html := string(data)
	if basePath != "" {
		// 在 <head> 标签后注入 <base> 标签
		baseTag := fmt.Sprintf(`<base href="%s/">`, basePath)
		html = strings.Replace(html, "<head>", "<head>\n    "+baseTag, 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
	return nil
}

func (a *App) serveFrontendNotBuilt(w http.ResponseWriter) error {
	writeJSON(w, http.StatusOK, map[string]string{"status": "frontend_not_built"})
	return nil
}

func insideDir(base, target string) bool {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

type CollectorConfig struct {
	Enabled              bool    `json:"enabled"`
	CLIProxyURL          string  `json:"cliaproxy_url"`
	ManagementKey        string  `json:"management_key"`
	QueueName            string  `json:"queue_name"`
	BatchSize            int     `json:"batch_size"`
	PollIntervalSeconds  float64 `json:"poll_interval_seconds"`
	RetryIntervalSeconds float64 `json:"retry_interval_seconds"`
}

type KeeperConfig struct {
	ScheduleCron                      string `json:"schedule_cron"`
	QuotaThreshold                    int    `json:"quota_threshold"`
	UsageTimeoutSeconds               int    `json:"usage_timeout_seconds"`
	CPATimeoutSeconds                 int    `json:"cpa_timeout_seconds"`
	MaxRetries                        int    `json:"max_retries"`
	WorkerThreads                     int    `json:"worker_threads"`
	ConditionalRefreshIntervalSeconds int    `json:"conditional_refresh_interval_seconds"`
	AccountRefreshCacheMinutes        int    `json:"account_refresh_cache_minutes"`
	DryRun                            bool   `json:"dry_run"`
	EnableCredentialWebsockets        bool   `json:"enable_credential_websockets"`
	AutoStartDaemon                   bool   `json:"auto_start_daemon"`
}

type LiteLLMProxyConfig struct {
	Enabled  bool   `json:"enabled"`
	ProxyURL string `json:"proxy_url"`
}

type AppConfig struct {
	Collector               CollectorConfig    `json:"collector"`
	CodexKeeper             KeeperConfig       `json:"codex_keeper"`
	CodexKeeperPriorityRule map[string]int     `json:"codex_keeper_priority_rules"`
	LiteLLMProxy            LiteLLMProxyConfig `json:"litellm_proxy"`
	ModelRequestURL         string             `json:"model_request_url"`
	SessionSecret           string             `json:"session_secret"`
	CPAMCUrl                string             `json:"cpamc_url"`
}

func defaultConfig() (AppConfig, error) {
	secret, err := createSecret()
	if err != nil {
		return AppConfig{}, err
	}
	return AppConfig{
		Collector: CollectorConfig{
			Enabled:              false,
			CLIProxyURL:          defaultCPAURL,
			ManagementKey:        "",
			QueueName:            "usage",
			BatchSize:            100,
			PollIntervalSeconds:  2.0,
			RetryIntervalSeconds: 10.0,
		},
		CodexKeeper: KeeperConfig{
			ScheduleCron:                      "*/30 * * * *",
			QuotaThreshold:                    100,
			UsageTimeoutSeconds:               30,
			CPATimeoutSeconds:                 30,
			MaxRetries:                        2,
			WorkerThreads:                     8,
			ConditionalRefreshIntervalSeconds: 30,
			AccountRefreshCacheMinutes:        10,
			DryRun:                            true,
			EnableCredentialWebsockets:        false,
			AutoStartDaemon:                   false,
		},
		CodexKeeperPriorityRule: clonePriorityRules(defaultKeeperPriorityRules),
		LiteLLMProxy: LiteLLMProxyConfig{
			Enabled:  false,
			ProxyURL: "",
		},
		ModelRequestURL: defaultCPAURL,
		SessionSecret:   secret,
		CPAMCUrl:        "",
	}, nil
}

func clonePriorityRules(input map[string]int) map[string]int {
	result := make(map[string]int, len(input))
	for key, value := range input {
		if strings.TrimSpace(key) != "" && value >= 0 && value <= 20 {
			result[strings.ToLower(strings.TrimSpace(key))] = value
		}
	}
	return result
}

func (a *App) loadConfig(ctx context.Context) (AppConfig, error) {
	row := a.db.QueryRowContext(ctx, `
		SELECT collector_enabled, cliaproxy_url, management_key, queue_name, batch_size,
		       poll_interval_seconds, retry_interval_seconds, codex_keeper_settings,
		       codex_keeper_priority_rules, litellm_proxy_enabled, litellm_proxy_url,
		       model_request_url, session_secret, cpamc_url
		FROM app_settings WHERE id = 1
	`)
	var collectorEnabled, litellmProxyEnabled bool
	var cliaproxyURL, managementKey, queueName, keeperJSON, rulesJSON, litellmProxyURL, modelRequestURL, sessionSecret, cpamcURL string
	var batchSize int
	var pollInterval, retryInterval float64
	if err := row.Scan(&collectorEnabled, &cliaproxyURL, &managementKey, &queueName, &batchSize, &pollInterval, &retryInterval, &keeperJSON, &rulesJSON, &litellmProxyEnabled, &litellmProxyURL, &modelRequestURL, &sessionSecret, &cpamcURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AppConfig{}, fmt.Errorf("%w: app_settings id=1 is missing; run `cpa-helper migrate`", ErrAppSettingsMissing)
		}
		return AppConfig{}, err
	}
	cfg, err := defaultConfig()
	if err != nil {
		return AppConfig{}, err
	}
	cfg.Collector = CollectorConfig{
		Enabled:              collectorEnabled,
		CLIProxyURL:          nonBlank(cliaproxyURL, defaultCPAURL),
		ManagementKey:        managementKey,
		QueueName:            nonBlank(queueName, "usage"),
		BatchSize:            clampInt(batchSize, 1, 1000, 100),
		PollIntervalSeconds:  clampFloat(pollInterval, 0.2, 3600, 2.0),
		RetryIntervalSeconds: clampFloat(retryInterval, 1, 3600, 10.0),
	}
	if strings.TrimSpace(keeperJSON) != "" {
		_ = json.Unmarshal([]byte(keeperJSON), &cfg.CodexKeeper)
		cfg.CodexKeeper = normalizeKeeperConfig(cfg.CodexKeeper)
	}
	if strings.TrimSpace(rulesJSON) != "" {
		var rules map[string]int
		if json.Unmarshal([]byte(rulesJSON), &rules) == nil {
			cfg.CodexKeeperPriorityRule = normalizePriorityRules(rules)
		}
	}
	if strings.TrimSpace(sessionSecret) != "" {
		cfg.SessionSecret = sessionSecret
	}
	cfg.LiteLLMProxy = LiteLLMProxyConfig{
		Enabled:  litellmProxyEnabled,
		ProxyURL: strings.TrimSpace(litellmProxyURL),
	}
	cfg.ModelRequestURL = nonBlank(strings.TrimRight(strings.TrimSpace(modelRequestURL), "/"), cfg.Collector.CLIProxyURL)
	cfg.CPAMCUrl = strings.TrimSpace(cpamcURL)
	return cfg, nil
}

func normalizeKeeperConfig(cfg KeeperConfig) KeeperConfig {
	if strings.TrimSpace(cfg.ScheduleCron) == "" {
		cfg.ScheduleCron = "*/30 * * * *"
	}
	cfg.QuotaThreshold = clampInt(cfg.QuotaThreshold, 0, 100, 100)
	cfg.UsageTimeoutSeconds = maxInt(cfg.UsageTimeoutSeconds, 1, 30)
	cfg.CPATimeoutSeconds = maxInt(cfg.CPATimeoutSeconds, 1, 30)
	cfg.MaxRetries = clampInt(cfg.MaxRetries, 0, 5, 2)
	cfg.WorkerThreads = clampInt(cfg.WorkerThreads, 1, 64, 8)
	if !validKeeperConditionalRefreshInterval(cfg.ConditionalRefreshIntervalSeconds) {
		cfg.ConditionalRefreshIntervalSeconds = 30
	}
	cfg.AccountRefreshCacheMinutes = maxInt(cfg.AccountRefreshCacheMinutes, 1, 10)
	return cfg
}

func validKeeperConditionalRefreshInterval(seconds int) bool {
	switch seconds {
	case 0, 5, 10, 30, 60:
		return true
	default:
		return false
	}
}

func normalizePriorityRules(input map[string]int) map[string]int {
	rules := clonePriorityRules(defaultKeeperPriorityRules)
	for rawKey, rawValue := range input {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if key != "" && rawValue >= 0 && rawValue <= 20 {
			rules[key] = rawValue
		}
	}
	return rules
}

func (a *App) saveConfig(ctx context.Context, cfg AppConfig) error {
	keeperBytes, err := json.Marshal(normalizeKeeperConfig(cfg.CodexKeeper))
	if err != nil {
		return err
	}
	rulesBytes, err := json.Marshal(normalizePriorityRules(cfg.CodexKeeperPriorityRule))
	if err != nil {
		return err
	}
	_, err = a.db.ExecContext(ctx, `
		UPDATE app_settings
		SET collector_enabled = ?, cliaproxy_url = ?, management_key = ?, queue_name = ?,
		    batch_size = ?, poll_interval_seconds = ?, retry_interval_seconds = ?,
		    codex_keeper_settings = ?, codex_keeper_priority_rules = ?,
		    litellm_proxy_enabled = ?, litellm_proxy_url = ?,
		    model_request_url = ?, session_secret = ?, cpamc_url = ?, updated_at = ?
		WHERE id = 1
	`, cfg.Collector.Enabled, strings.TrimRight(strings.TrimSpace(cfg.Collector.CLIProxyURL), "/"), strings.TrimSpace(cfg.Collector.ManagementKey), strings.TrimSpace(cfg.Collector.QueueName), cfg.Collector.BatchSize, cfg.Collector.PollIntervalSeconds, cfg.Collector.RetryIntervalSeconds, string(keeperBytes), string(rulesBytes), cfg.LiteLLMProxy.Enabled, strings.TrimSpace(cfg.LiteLLMProxy.ProxyURL), strings.TrimRight(strings.TrimSpace(cfg.ModelRequestURL), "/"), cfg.SessionSecret, strings.TrimSpace(cfg.CPAMCUrl), dbTime(time.Now()))
	return err
}

func (a *App) runMigrations(ctx context.Context) error {
	goose.SetBaseFS(backendMigrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	return goose.UpContext(ctx, a.db, ".")
}

func dbTime(t time.Time) string {
	return t.In(appTimeLocation).Format("2006-01-02T15:04:05.999999-07:00")
}

func dbTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dbTime(*t)
}

func apiDateTime(t time.Time) string {
	return t.In(appTimeLocation).Format("2006-01-02T15:04:05-07:00")
}

func apiDateTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := apiDateTime(*t)
	return &formatted
}

func apiDateTimes(times []time.Time) []string {
	formatted := make([]string, 0, len(times))
	for _, value := range times {
		formatted = append(formatted, apiDateTime(value))
	}
	return formatted
}

var timeType = reflect.TypeOf(time.Time{})

func apiJSONValue(value any) any {
	return apiJSONReflectValue(reflect.ValueOf(value))
}

func apiJSONReflectValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == timeType {
		timestamp := value.Interface().(time.Time)
		if timestamp.IsZero() {
			return nil
		}
		return apiDateTime(timestamp)
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return apiJSONReflectValue(value.Elem())
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		result := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key()
			if key.Kind() != reflect.String {
				continue
			}
			result[key.String()] = apiJSONReflectValue(iter.Value())
		}
		return result
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			if value.Kind() == reflect.Slice && value.IsNil() {
				return nil
			}
			return value.Interface()
		}
		if value.Kind() == reflect.Slice && value.IsNil() {
			return []any{}
		}
		result := make([]any, 0, value.Len())
		for i := 0; i < value.Len(); i++ {
			result = append(result, apiJSONReflectValue(value.Index(i)))
		}
		return result
	case reflect.Struct:
		return apiJSONStructValue(value)
	default:
		if value.CanInterface() {
			return value.Interface()
		}
		return nil
	}
}

func apiJSONStructValue(value reflect.Value) map[string]any {
	result := map[string]any{}
	valueType := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := valueType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name, omitEmpty, ok := jsonFieldName(field)
		if !ok {
			continue
		}
		fieldValue := value.Field(i)
		if omitEmpty && fieldValue.IsZero() {
			continue
		}
		result[name] = apiJSONReflectValue(fieldValue)
	}
	return result
}

func jsonFieldName(field reflect.StructField) (string, bool, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	name, options, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	omitEmpty := false
	for _, option := range strings.Split(options, ",") {
		if option == "omitempty" {
			omitEmpty = true
			break
		}
	}
	return name, omitEmpty, true
}

func parseDBTime(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	if hasExplicitTimeZone(text) {
		for _, layout := range zonedTimeLayouts() {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.In(appTimeLocation), true
			}
		}
	}
	return parseDBWallClockTime(text)
}

func zonedTimeLayouts() []string {
	return []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05.999999-0700",
		"2006-01-02T15:04:05-0700",
		"2006-01-02 15:04:05.999999999-0700",
		"2006-01-02 15:04:05.999999-0700",
		"2006-01-02 15:04:05-0700",
	}
}

func parseInputTime(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	if hasExplicitTimeZone(text) {
		for _, layout := range zonedTimeLayouts() {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.In(appTimeLocation), true
			}
		}
	}
	return parseDBWallClockTime(text)
}

func hasExplicitTimeZone(value string) bool {
	text := strings.TrimSpace(value)
	if len(text) <= 10 {
		return false
	}
	tail := text[10:]
	return strings.HasSuffix(tail, "Z") || strings.HasSuffix(tail, "z") ||
		strings.Contains(tail, "+") || strings.Contains(tail, "-")
}

func parseDBWallClockTime(value string) (time.Time, bool) {
	text := strings.TrimSpace(strings.Replace(value, "T", " ", 1))
	for index := 10; index < len(text); index++ {
		switch text[index] {
		case 'Z', 'z', '+', '-':
			text = strings.TrimSpace(text[:index])
			index = len(text)
		}
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, text, appTimeLocation); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func timePtr(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, ok := parseDBTime(value.String)
	if !ok {
		return nil
	}
	return &parsed
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func createSalt() (string, error) {
	return security.CreateSalt()
}

func createSecret() (string, error) {
	return security.CreateSecret()
}

func hashPassword(password, salt string) string {
	return security.HashPassword(password, salt)
}

func verifyPassword(password, salt, expected string) bool {
	return security.VerifyPassword(password, salt, expected)
}

func hashAPIKey(apiKey string) string {
	return security.HashAPIKey(apiKey)
}

func maskSecret(value *string) string {
	return security.MaskSecret(value)
}

func readSessionToken(token, secret string) (*security.Identity, bool) {
	return security.ReadSessionToken(token, secret)
}

type AuthUser struct {
	ID                 int    `json:"id"`
	Username           string `json:"username"`
	IsAdmin            bool   `json:"is_admin"`
	MustChangePassword bool   `json:"must_change_password"`
}

func (a *App) currentUser(ctx context.Context, r *http.Request) (*AuthUser, error) {
	cfg, err := a.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	cookie, err := r.Cookie(security.SessionCookieName())
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return nil, authenticationError("请先登录")
	}
	identity, ok := readSessionToken(cookie.Value, cfg.SessionSecret)
	if !ok {
		return nil, authenticationError("登录会话已失效")
	}
	var row *sql.Row
	if identity.UserID != nil {
		row = a.db.QueryRowContext(ctx, `SELECT id, username, is_admin FROM users WHERE id = ? AND disabled_at IS NULL`, *identity.UserID)
	} else if identity.Username != nil {
		row = a.db.QueryRowContext(ctx, `SELECT id, username, is_admin FROM users WHERE username = ? AND disabled_at IS NULL`, *identity.Username)
	} else {
		return nil, authenticationError("登录会话已失效")
	}
	user := AuthUser{}
	if err := row.Scan(&user.ID, &user.Username, &user.IsAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, authenticationError("登录会话已失效")
		}
		return nil, err
	}
	return &user, nil
}

func (a *App) readyUser(ctx context.Context, r *http.Request) (*AuthUser, error) {
	user, err := a.currentUser(ctx, r)
	if err != nil {
		return nil, err
	}
	if user.MustChangePassword {
		return nil, forbiddenError("首次登录后必须先修改账号密码")
	}
	return user, nil
}

func (a *App) adminUser(ctx context.Context, r *http.Request) (*AuthUser, error) {
	user, err := a.readyUser(ctx, r)
	if err != nil {
		return nil, err
	}
	if !user.IsAdmin {
		return nil, forbiddenError("需要管理员权限")
	}
	return user, nil
}

func setSessionCookie(w http.ResponseWriter, userID int, secret string) error {
	return security.SetSessionCookie(w, userID, secret)
}

func clearSessionCookie(w http.ResponseWriter) {
	security.ClearSessionCookie(w)
}

func nonBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func clampInt(value, minValue, maxValue, fallback int) int {
	if value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func maxInt(value, minValue, fallback int) int {
	if value < minValue {
		return fallback
	}
	return value
}

func clampFloat(value, minValue, maxValue, fallback float64) float64 {
	if math.IsNaN(value) || value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func parseIntPath(value string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || id <= 0 {
		return 0, notFoundError("资源不存在")
	}
	return id, nil
}

func splitPath(path, prefix string) []string {
	// 支持带 base path 的路径
	// 例如：/cpa-helper/api/codex-keeper/settings -> settings
	//      /api/codex-keeper/settings -> settings
	if idx := strings.Index(path, prefix); idx >= 0 {
		path = path[idx+len(prefix):]
	}
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func readAllAndRestore(r *http.Request) []byte {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func httpClient(timeout time.Duration) *http.Client {
	return cpahttp.Client(timeout)
}

func managementHeaders(key string) http.Header {
	return cpahttp.ManagementHeaders(key)
}

func makeURL(baseURL, path string, query url.Values) string {
	return cpahttp.MakeURL(baseURL, path, query)
}

func doJSON(ctx context.Context, client *http.Client, method, target string, headers http.Header, body any) (*http.Response, []byte, error) {
	return cpahttp.DoJSON(ctx, client, method, target, headers, body)
}

func ensureHTTPSURL(sourceURL string) error {
	if err := cpahttp.EnsureHTTPSURL(sourceURL); err != nil {
		return validationError("URL 必须是有效的 HTTP/HTTPS 地址")
	}
	return nil
}

func parseCronExpression(expression string) (cron.Schedule, string, error) {
	normalized := strings.Join(strings.Fields(expression), " ")
	if len(strings.Fields(normalized)) != 5 {
		return nil, normalized, validationError("Cron 表达式无效，请使用 5 段格式：分 时 日 月 周")
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(normalized)
	if err != nil {
		return nil, normalized, validationError("Cron 表达式无效，请使用 5 段格式：分 时 日 月 周")
	}
	if spec, ok := schedule.(*cron.SpecSchedule); ok {
		spec.Location = appTimeLocation
	}
	return schedule, normalized, nil
}

func nextRunTimes(expression string, count int, base time.Time) ([]time.Time, string, error) {
	schedule, normalized, err := parseCronExpression(expression)
	if err != nil {
		return nil, normalized, err
	}
	if count <= 0 {
		count = 5
	}
	times := make([]time.Time, 0, count)
	next := base.In(appTimeLocation)
	for i := 0; i < count; i++ {
		next = schedule.Next(next).In(appTimeLocation)
		times = append(times, next)
	}
	return times, normalized, nil
}

func sortedPriorityRules(rules map[string]int) []map[string]any {
	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		items = append(items, map[string]any{"account_type": key, "priority": rules[key]})
	}
	return items
}
