package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConsumeRespQueueUsesHTTPManagementUsageQueue(t *testing.T) {
	requested := false
	cpa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.URL.Path != "/v0/management/usage-queue" {
			t.Fatalf("path = %q, want /v0/management/usage-queue", r.URL.Path)
		}
		if got := r.URL.Query().Get("count"); got != "2" {
			t.Fatalf("count query = %q, want 2", got)
		}
		if got := r.Header.Get("X-Management-Key"); got != "test-management-key" {
			t.Fatalf("management header = %q, want test-management-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]any{
			map[string]any{"request_id": "req-http", "total_tokens": 12},
			`{"request_id":"req-string"}`,
			nil,
		})
	}))
	defer cpa.Close()

	items, err := consumeRespQueue(context.Background(), CollectorConfig{
		CLIProxyURL:   cpa.URL,
		ManagementKey: "test-management-key",
		QueueName:     "usage",
		BatchSize:     2,
	})
	if err != nil {
		t.Fatalf("consumeRespQueue returned error: %v", err)
	}
	if !requested {
		t.Fatal("HTTP usage queue endpoint was not requested")
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2: %#v", len(items), items)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(items[0]), &first); err != nil {
		t.Fatalf("first item is not JSON object: %q", items[0])
	}
	if first["request_id"] != "req-http" {
		t.Fatalf("first request_id = %#v, want req-http", first["request_id"])
	}
	if items[1] != `{"request_id":"req-string"}` {
		t.Fatalf("second item = %q, want encoded string payload", items[1])
	}
}

func TestUsesRespQueueProtocolOnlyForExplicitRawProtocols(t *testing.T) {
	tests := map[string]bool{
		"https://api.example.com":     false,
		"http://127.0.0.1:8317":       false,
		"api.example.com:8317":        false,
		"tcp://127.0.0.1:8317":        true,
		"redis://127.0.0.1:8317":      true,
		"resp://127.0.0.1:8317":       true,
		"wss://api.example.com/ws":    false,
		"https://api.example.com:443": false,
	}
	for rawURL, want := range tests {
		if got := usesRespQueueProtocol(rawURL); got != want {
			t.Fatalf("usesRespQueueProtocol(%q) = %v, want %v", rawURL, got, want)
		}
	}
}
