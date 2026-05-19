package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecordCostUsesClaudeCacheReadAndCreationTokens(t *testing.T) {
	provider := "claude"
	model := "claude-sonnet-test"
	record := UsageRecord{
		Provider:            &provider,
		Model:               &model,
		InputTokens:         100,
		OutputTokens:        10,
		CachedTokens:        999,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		TotalTokens:         110,
	}
	prices := map[[2]string]ModelPrice{
		priceKey("anthropic", model): {
			Provider:                   "anthropic",
			Model:                      model,
			InputUSDPerMillion:         10,
			OutputUSDPerMillion:        20,
			CacheReadUSDPerMillion:     1,
			CacheCreationUSDPerMillion: 12,
		},
	}

	amount, unpriced := recordCost(record, prices)
	if unpriced {
		t.Fatal("record should be priced")
	}
	want := mathRound((100*10+20*1+30*12+10*20)/1_000_000.0, 8)
	if amount != want {
		t.Fatalf("cost = %v, want %v", amount, want)
	}
}

func TestRecordCostTruncatesGenericCachedTokens(t *testing.T) {
	provider := "openai"
	model := "gpt-test"
	record := UsageRecord{
		Provider:     &provider,
		Model:        &model,
		InputTokens:  100,
		OutputTokens: 10,
		CachedTokens: 150,
		TotalTokens:  110,
	}
	prices := map[[2]string]ModelPrice{
		priceKey(provider, model): {
			Provider:               provider,
			Model:                  model,
			InputUSDPerMillion:     10,
			OutputUSDPerMillion:    20,
			CacheReadUSDPerMillion: 1,
		},
	}

	amount, unpriced := recordCost(record, prices)
	if unpriced {
		t.Fatal("record should be priced")
	}
	want := mathRound((100*1+10*20)/1_000_000.0, 8)
	if amount != want {
		t.Fatalf("cost = %v, want %v", amount, want)
	}
}

func TestUsageAggregatesClaudeCacheReadAndCreationTokens(t *testing.T) {
	provider := "claude"
	model := "claude-sonnet-test"
	record := UsageRecord{
		Timestamp:           time.Date(2026, 5, 19, 10, 0, 0, 0, appTimeLocation),
		Provider:            &provider,
		Model:               &model,
		InputTokens:         10,
		OutputTokens:        5,
		CachedTokens:        20,
		CacheReadTokens:     20,
		CacheCreationTokens: 30,
		ReasoningTokens:     7,
		TotalTokens:         15,
	}
	prices := map[[2]string]ModelPrice{
		priceKey("anthropic", model): {
			Provider:                   "anthropic",
			Model:                      model,
			InputUSDPerMillion:         1,
			OutputUSDPerMillion:        2,
			CacheReadUSDPerMillion:     0.5,
			CacheCreationUSDPerMillion: 1.25,
		},
	}
	filters := UsageFilters{}
	start := time.Date(2026, 5, 19, 0, 0, 0, 0, appTimeLocation)
	end := start.Add(24 * time.Hour)
	filters.Start = &start
	filters.End = &end

	summary := usageSummaryFromRecords(filters, []UsageRecord{record}, prices)
	if summary["input_tokens"].(int) != 60 {
		t.Fatalf("summary input = %v, want 60", summary["input_tokens"])
	}
	if summary["total_tokens"].(int) != 72 {
		t.Fatalf("summary total = %v, want 72", summary["total_tokens"])
	}
	trends := trendPointsFromRecords(filters, []UsageRecord{record}, prices)
	if len(trends) != 1 || trends[0]["total_tokens"].(int) != 72 {
		t.Fatalf("trend totals = %#v, want one item with total 72", trends)
	}
	ranking := rankingFromRecords([]UsageRecord{record}, prices, "model", nil)
	items := ranking["items"].([]map[string]any)
	if len(items) != 1 || items[0]["total_tokens"].(int) != 72 {
		t.Fatalf("ranking totals = %#v, want one item with total 72", items)
	}
	distributions := distributionsFromRecords([]UsageRecord{record}, prices)
	models := distributions["models"].([]map[string]any)
	if len(models) != 1 || models[0]["total_tokens"].(int) != 72 {
		t.Fatalf("distribution totals = %#v, want one item with total 72", models)
	}
}

func TestSyncLiteLLMPricesReplacesLiteLLMSource(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	now := dbTime(time.Now().In(appTimeLocation))
	if _, err := app.db.Exec(`
		INSERT INTO model_prices (
			provider, model, input_usd_per_million, output_usd_per_million,
			cache_read_usd_per_million, cache_creation_usd_per_million, source, updated_at
		) VALUES
			('openai', 'old-litellm-model', 1, 1, 1, 1, 'litellm', ?),
			('openai', 'manual-model', 9, 9, 9, 9, 'manual', ?)
	`, now, now); err != nil {
		t.Fatalf("seed prices: %v", err)
	}

	rawData := map[string]any{
		"gpt-new-model": map[string]any{
			"litellm_provider":            "openai",
			"input_cost_per_token":        0.000001,
			"output_cost_per_token":       0.000002,
			"cache_read_input_token_cost": 0.0000001,
		},
		"claude-new-model": map[string]any{
			"litellm_provider":                "anthropic",
			"input_cost_per_token":            0.000003,
			"output_cost_per_token":           0.000015,
			"cache_read_input_token_cost":     0.0000003,
			"cache_creation_input_token_cost": 0.00000375,
		},
		"manual-model": map[string]any{
			"litellm_provider":     "openai",
			"input_cost_per_token": 0.000001,
		},
	}
	result, err := app.syncLiteLLMPrices(context.Background(), "https://example.com/prices.json", rawData)
	if err != nil {
		t.Fatalf("syncLiteLLMPrices failed: %v", err)
	}
	if result["imported"].(int) != 2 || result["skipped_manual"].(int) != 1 {
		t.Fatalf("sync result = %#v, want imported 2 skipped_manual 1", result)
	}

	var oldCount int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM model_prices WHERE source = 'litellm' AND model = 'old-litellm-model'`).Scan(&oldCount); err != nil {
		t.Fatalf("query old litellm count: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("old litellm rows = %d, want 0", oldCount)
	}
	var manualInput float64
	if err := app.db.QueryRow(`SELECT input_usd_per_million FROM model_prices WHERE source = 'manual' AND model = 'manual-model'`).Scan(&manualInput); err != nil {
		t.Fatalf("query manual price: %v", err)
	}
	if manualInput != 9 {
		t.Fatalf("manual price = %v, want preserved 9", manualInput)
	}
	var cacheRead, cacheCreation float64
	if err := app.db.QueryRow(`SELECT cache_read_usd_per_million, cache_creation_usd_per_million FROM model_prices WHERE source = 'litellm' AND model = 'claude-new-model'`).Scan(&cacheRead, &cacheCreation); err != nil {
		t.Fatalf("query claude price: %v", err)
	}
	if cacheRead != 0.3 || cacheCreation != 3.75 {
		t.Fatalf("claude cache prices = read %v creation %v, want 0.3 and 3.75", cacheRead, cacheCreation)
	}
}

func TestLiteLLMSyncUsesConfiguredHTTPProxy(t *testing.T) {
	t.Setenv("CPA_HELPER_DATA_DIR", t.TempDir())
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		http.Error(w, "direct request should not be used", http.StatusBadGateway)
	}))
	defer target.Close()

	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls++
		if r.URL.String() != target.URL+"/prices.json" {
			t.Errorf("proxied request URL = %q, want %q", r.URL.String(), target.URL+"/prices.json")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"proxy-model": map[string]any{
				"litellm_provider":     "openai",
				"input_cost_per_token": 0.000001,
			},
		})
	}))
	defer proxy.Close()

	app, err := New()
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer app.Close()

	handler := app.Routes()
	cookies := requestJSONForPricingTest(t, handler, http.MethodPost, "/api/auth/setup", map[string]any{
		"username": "admin",
		"password": "test-password",
		"nickname": "Admin",
	}, nil, nil)

	var settings struct {
		Enabled  bool   `json:"enabled"`
		ProxyURL string `json:"proxy_url"`
	}
	requestJSONForPricingTest(t, handler, http.MethodGet, "/api/model-prices/litellm-proxy", nil, cookies, &settings)
	if settings.Enabled || settings.ProxyURL != "" {
		t.Fatalf("default proxy settings = %#v, want disabled empty proxy", settings)
	}
	requestJSONForPricingTest(t, handler, http.MethodPut, "/api/model-prices/litellm-proxy", map[string]any{
		"enabled":   true,
		"proxy_url": proxy.URL,
	}, cookies, &settings)
	if !settings.Enabled || settings.ProxyURL != proxy.URL {
		t.Fatalf("saved proxy settings = %#v, want enabled %q", settings, proxy.URL)
	}

	var syncResult struct {
		Imported int `json:"imported"`
	}
	requestJSONForPricingTest(t, handler, http.MethodPost, "/api/model-prices/sync/litellm", map[string]any{
		"source_url": target.URL + "/prices.json",
	}, cookies, &syncResult)
	if syncResult.Imported != 1 {
		t.Fatalf("imported = %d, want 1", syncResult.Imported)
	}
	if proxyCalls != 1 || targetCalls != 0 {
		t.Fatalf("proxy/direct calls = %d/%d, want 1/0", proxyCalls, targetCalls)
	}
}

func TestNormalizeLiteLLMProxyURLAcceptsSock5Alias(t *testing.T) {
	normalized, err := normalizeLiteLLMProxyURL("sock5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("normalizeLiteLLMProxyURL failed: %v", err)
	}
	if normalized != "socks5://127.0.0.1:1080" {
		t.Fatalf("normalized proxy URL = %q, want socks5://127.0.0.1:1080", normalized)
	}
}

func requestJSONForPricingTest(
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
