package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	backendApp "cpa-helper/backend/internal/app"
)

type keeperStatusResponse struct {
	Running       bool     `json:"running"`
	DaemonRunning bool     `json:"daemon_running"`
	Logs          []string `json:"logs"`
}

func TestKeeperAutoStartReportsDaemonRunning(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer func() {
		if app != nil {
			app.Close()
		}
	}()

	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     "http://127.0.0.1:1",
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)
	requestJSON(t, handler, http.MethodPut, "/api/codex-keeper/settings", map[string]any{
		"schedule_cron":     "0 0 29 2 *",
		"auto_start_daemon": true,
	}, cookies, nil)
	app.Close()
	app = nil

	app, err = backendApp.New()
	if err != nil {
		t.Fatalf("New() with auto-start enabled failed: %v", err)
	}
	handler = app.Routes()

	status := keeperStatusResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/codex-keeper/status", nil, cookies, &status)
	if !status.DaemonRunning {
		t.Fatal("daemon_running = false, want true after auto-start")
	}
	if status.Running {
		t.Fatal("running = true, want false while daemon is only waiting for the next cron tick")
	}

	requestJSON(t, handler, http.MethodPost, "/api/codex-keeper/stop", nil, cookies, nil)
	status = keeperStatusResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/codex-keeper/status", nil, cookies, &status)
	if status.DaemonRunning {
		t.Fatal("daemon_running = true, want false after stop")
	}
}

func TestKeeperLogsUseStandardFileFormatAndCanBeCleared(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	requestJSON(t, handler, http.MethodPut, "/api/settings", map[string]any{
		"cliaproxy_url":     "http://127.0.0.1:1",
		"management_key":    "test-management-key",
		"collector_enabled": false,
	}, cookies, nil)
	requestJSON(t, handler, http.MethodPut, "/api/codex-keeper/settings", map[string]any{
		"schedule_cron": "0 0 29 2 *",
	}, cookies, nil)
	requestJSON(t, handler, http.MethodPost, "/api/codex-keeper/start", nil, cookies, nil)

	status := keeperStatusResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/codex-keeper/status", nil, cookies, &status)
	if len(status.Logs) == 0 {
		t.Fatal("status logs are empty, want daemon start log")
	}
	assertStandardKeeperLogLine(t, status.Logs[len(status.Logs)-1])

	logPath := filepath.Join(dataDir, "logs", "codex-keeper-"+time.Now().Format("2006-01-02")+".log")
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read keeper log file: %v", err)
	}
	fileLines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(fileLines) == 0 {
		t.Fatal("keeper log file is empty")
	}
	assertStandardKeeperLogLine(t, strings.TrimSpace(fileLines[len(fileLines)-1]))

	requestJSON(t, handler, http.MethodPost, "/api/codex-keeper/logs/clear", nil, cookies, nil)
	matches, err := filepath.Glob(filepath.Join(dataDir, "logs", "codex-keeper-*.log"))
	if err != nil {
		t.Fatalf("glob keeper log files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("keeper log files after clear = %v, want none", matches)
	}
}

func TestKeeperStatusRestoresRecentLogFileLines(t *testing.T) {
	dataDir := t.TempDir()
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	expected := "2026-05-16 16:03:05,571 - app.services.codex_keeper_service - INFO - demo.json: 巡检正常，类型 free"
	if err := os.WriteFile(filepath.Join(logDir, "codex-keeper-2026-05-16.log"), []byte(expected+"\n"), 0o644); err != nil {
		t.Fatalf("write keeper log fixture: %v", err)
	}
	t.Setenv("CPA_HELPER_DATA_DIR", dataDir)

	app, err := backendApp.New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSON(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)
	status := keeperStatusResponse{}
	requestJSON(t, handler, http.MethodGet, "/api/codex-keeper/status", nil, cookies, &status)
	if len(status.Logs) != 1 || status.Logs[0] != expected {
		t.Fatalf("restored logs = %#v, want %#v", status.Logs, []string{expected})
	}
}

func assertStandardKeeperLogLine(t *testing.T, line string) {
	t.Helper()
	pattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3} - app\.services\.codex_keeper_service - INFO - .+`)
	if !pattern.MatchString(line) {
		t.Fatalf("keeper log line %q does not match standard format", line)
	}
}

func requestJSON(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	cookies []*http.Cookie,
	target any,
) []*http.Cookie {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
			t.Fatalf("decode %s %s response: %v", method, path, err)
		}
	}
	return append(cookies, recorder.Result().Cookies()...)
}
