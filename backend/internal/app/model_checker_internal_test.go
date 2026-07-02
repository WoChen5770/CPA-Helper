package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelCheckerRetriesRequestFailureBeforeMarkingAvailable(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	var calls atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := calls.Add(1)
		if attempt == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack failed: %v", err)
			}
			_ = conn.Close()
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "pong"}},
			},
		})
	}))
	defer cpa.Close()

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions failed: %v", err)
	}
	defer app.Close()

	cfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.ModelRequestURL = cpa.URL
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if err := app.saveModelCheckerConfig(context.Background(), ModelCheckerConfig{
		TimeoutSeconds: 2,
		TestAPIKey:     "sk-test-model-checker",
		TestQuestions:  []string{"ping"},
	}); err != nil {
		t.Fatalf("saveModelCheckerConfig failed: %v", err)
	}

	runner := newModelCheckRunner(app)
	statusCode := -1
	content := ""
	question := ""
	result := runner.checkSingleModelWithLog(context.Background(), trackedModel{
		ModelID:  "openai/gpt-test",
		Provider: "openai",
	}, ModelCheckerConfig{
		TimeoutSeconds: 2,
		TestAPIKey:     "sk-test-model-checker",
		TestQuestions:  []string{"ping"},
	}, &statusCode, &content, &question)

	if got := calls.Load(); got != 2 {
		t.Fatalf("request attempts = %d, want 2", got)
	}
	if result.Status != "available" {
		t.Fatalf("result status = %q, want available", result.Status)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want %d", statusCode, http.StatusOK)
	}
	if content != "pong" {
		t.Fatalf("content = %q, want pong", content)
	}
	if question != "ping" {
		t.Fatalf("question = %q, want ping", question)
	}
}

func TestModelCheckerMarksRepeatedRequestFailureAsError(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())

	var calls atomic.Int32
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("hijack failed: %v", err)
		}
		_ = conn.Close()
	}))
	defer cpa.Close()

	app, err := NewWithOptions(context.Background(), NewOptions{Migrate: true})
	if err != nil {
		t.Fatalf("NewWithOptions failed: %v", err)
	}
	defer app.Close()

	cfg, err := app.loadConfig(context.Background())
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	cfg.ModelRequestURL = cpa.URL
	if err := app.saveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}
	if err := app.saveModelCheckerConfig(context.Background(), ModelCheckerConfig{
		TimeoutSeconds: 2,
		TestAPIKey:     "sk-test-model-checker",
		TestQuestions:  []string{"ping"},
	}); err != nil {
		t.Fatalf("saveModelCheckerConfig failed: %v", err)
	}

	runner := newModelCheckRunner(app)
	statusCode := -1
	content := ""
	question := ""
	result := runner.checkSingleModelWithLog(context.Background(), trackedModel{
		ModelID:  "openai/gpt-test",
		Provider: "openai",
	}, ModelCheckerConfig{
		TimeoutSeconds: 2,
		TestAPIKey:     "sk-test-model-checker",
		TestQuestions:  []string{"ping"},
	}, &statusCode, &content, &question)

	if got := calls.Load(); got != 2 {
		t.Fatalf("request attempts = %d, want 2", got)
	}
	if result.Status != "error" {
		t.Fatalf("result status = %q, want error", result.Status)
	}
	if statusCode != 0 {
		t.Fatalf("statusCode = %d, want 0", statusCode)
	}
	if content != "" {
		t.Fatalf("content = %q, want empty", content)
	}
	if question != "ping" {
		t.Fatalf("question = %q, want ping", question)
	}
}

func TestModelCheckerClassifiesStatusCodeZeroAsError(t *testing.T) {
	runner := &ModelCheckRunner{}
	statusCode := 0
	content := ""
	question := "ping"
	result := runner.checkSingleModelWithLog(context.Background(), trackedModel{}, ModelCheckerConfig{}, &statusCode, &content, &question)
	_ = time.Second
	if result.Status == "unavailable" {
		t.Fatal("statusCode 0 should not be classified as unavailable")
	}
}
