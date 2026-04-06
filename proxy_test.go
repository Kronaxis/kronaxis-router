package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockBackendServer creates a test HTTP server that mimics an OpenAI-compatible LLM backend.
func mockBackendServer(response string, statusCode int, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		if r.URL.Path == "/v1/models" || r.URL.Path == "/health" {
			w.WriteHeader(200)
			w.Write([]byte(`{"data":[{"id":"test"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		w.Write([]byte(response))
	}))
}

func validChatResponse(content string) string {
	resp := ChatResponse{
		ID:      "test-id",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "test-model",
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: content},
			FinishReason: "stop",
		}},
		Usage: &ChatUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

func setupTestRouter(backends []BackendConfig, rules []RoutingRule) {
	pool = newBackendPool(backends)
	rtr = newRouter(rules, DefaultsConfig{FallbackChain: testBackendNames(backends)}, pool)
	bat = newBatcher(BatchingConfig{Enabled: false})
	costs = newCostTracker(nil, nil)
	respCache = newResponseCache(100, 3600)
	rateLim = newRateLimiter(nil)
	batchMgr = newBatchManager("")
	cfg = &Config{
		Server: ServerConfig{
			Branding: BrandingConfig{Headers: true, HeaderName: "Test Router"},
		},
	}
}

func testBackendNames(configs []BackendConfig) []string {
	names := make([]string, len(configs))
	for i, c := range configs {
		names[i] = c.Name
	}
	return names
}

func TestProxyHandler_BasicRequest(t *testing.T) {
	mock := mockBackendServer(validChatResponse("Hello from test"), 200, 0)
	defer mock.Close()

	setupTestRouter(
		[]BackendConfig{{Name: "test", URL: mock.URL, Type: "vllm", MaxConcurrent: 10}},
		[]RoutingRule{{Name: "default", Priority: 100, Match: RuleMatch{}, Backends: []string{"test"}}},
	)

	body := `{"model":"test","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kronaxis-Service", "test-svc")

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp ChatResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.Content != "Hello from test" {
		t.Errorf("unexpected content: %s", resp.Choices[0].Message.Content)
	}

	// Check branding headers
	if rr.Header().Get("X-Powered-By") != "Test Router" {
		t.Errorf("expected X-Powered-By header, got %q", rr.Header().Get("X-Powered-By"))
	}
}

func TestProxyHandler_Failover(t *testing.T) {
	// First backend returns 500, second succeeds
	bad := mockBackendServer(`{"error":"internal"}`, 500, 0)
	defer bad.Close()
	good := mockBackendServer(validChatResponse("failover success"), 200, 0)
	defer good.Close()

	setupTestRouter(
		[]BackendConfig{
			{Name: "bad", URL: bad.URL, Type: "vllm", MaxConcurrent: 10},
			{Name: "good", URL: good.URL, Type: "vllm", MaxConcurrent: 10},
		},
		[]RoutingRule{{Name: "test", Priority: 100, Match: RuleMatch{}, Backends: []string{"bad", "good"}}},
	)

	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200 after failover, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp ChatResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "failover success" {
		t.Error("failover did not route to good backend")
	}
}

func TestProxyHandler_AllBackendsFail(t *testing.T) {
	bad := mockBackendServer(`{"error":"down"}`, 500, 0)
	defer bad.Close()

	setupTestRouter(
		[]BackendConfig{{Name: "bad", URL: bad.URL, Type: "vllm", MaxConcurrent: 10}},
		[]RoutingRule{{Name: "test", Priority: 100, Match: RuleMatch{}, Backends: []string{"bad"}}},
	)

	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	if rr.Code != 502 {
		t.Fatalf("expected 502 when all backends fail, got %d", rr.Code)
	}
}

func TestProxyHandler_BudgetReject(t *testing.T) {
	mock := mockBackendServer(validChatResponse("should not reach"), 200, 0)
	defer mock.Close()

	setupTestRouter(
		[]BackendConfig{{Name: "test", URL: mock.URL, Type: "vllm", MaxConcurrent: 10}},
		[]RoutingRule{{Name: "test", Priority: 100, Match: RuleMatch{}, Backends: []string{"test"}}},
	)
	costs = newCostTracker(map[string]BudgetConfig{
		"expensive-svc": {DailyLimitUSD: 0.001, Action: "reject"},
	}, nil)
	costs.recordCost("expensive-svc", 1.00) // Over budget

	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kronaxis-Service", "expensive-svc")

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	if rr.Code != 429 {
		t.Fatalf("expected 429 for budget rejection, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestProxyHandler_CacheHit(t *testing.T) {
	callCount := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(200)
			return
		}
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(validChatResponse(fmt.Sprintf("response-%d", callCount))))
	}))
	defer mock.Close()

	setupTestRouter(
		[]BackendConfig{{Name: "test", URL: mock.URL, Type: "vllm", MaxConcurrent: 10}},
		[]RoutingRule{{Name: "test", Priority: 100, Match: RuleMatch{}, Backends: []string{"test"}}},
	)

	// Temperature 0 = cacheable
	body := `{"model":"test","messages":[{"role":"user","content":"deterministic"}],"temperature":0}`

	// First request: cache miss
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	handleChatCompletions(rr1, req1)

	if rr1.Code != 200 {
		t.Fatalf("first request failed: %d", rr1.Code)
	}
	if rr1.Header().Get("X-Kronaxis-Cache") == "HIT" {
		t.Error("first request should not be a cache hit")
	}

	// Second identical request: cache hit
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	handleChatCompletions(rr2, req2)

	if rr2.Code != 200 {
		t.Fatalf("second request failed: %d", rr2.Code)
	}
	if rr2.Header().Get("X-Kronaxis-Cache") != "HIT" {
		t.Error("second request should be a cache hit")
	}

	// Backend should only have been called once
	if callCount != 1 {
		t.Errorf("backend called %d times, expected 1 (cache should prevent second call)", callCount)
	}
}

func TestProxyHandler_ThinkTagStripping(t *testing.T) {
	mock := mockBackendServer(validChatResponse("<think>reasoning</think>The answer is 42"), 200, 0)
	defer mock.Close()

	setupTestRouter(
		[]BackendConfig{{Name: "test", URL: mock.URL, Type: "vllm", MaxConcurrent: 10}},
		[]RoutingRule{{Name: "test", Priority: 100, Match: RuleMatch{}, Backends: []string{"test"}}},
	)

	body := `{"model":"test","messages":[{"role":"user","content":"what is 6*7?"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)

	var resp ChatResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Choices) == 0 {
		t.Fatal("no choices")
	}
	content := resp.Choices[0].Message.Content.(string)
	if strings.Contains(content, "<think>") {
		t.Error("think tags should be stripped")
	}
	if !strings.Contains(content, "42") {
		t.Errorf("expected '42' in response, got: %s", content)
	}
}

func TestProxyHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)
	if rr.Code != 405 {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestProxyHandler_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	setupTestRouter(nil, nil)
	handleChatCompletions(rr, req)
	if rr.Code != 400 {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestProxyHandler_NoBackends(t *testing.T) {
	setupTestRouter(nil, nil)

	body := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handleChatCompletions(rr, req)
	if rr.Code != 503 {
		t.Errorf("expected 503 with no backends, got %d", rr.Code)
	}
}

func TestRateLimiting(t *testing.T) {
	rateLim = newRateLimiter(map[string]RateLimitConfig{
		"limited": {RequestsPerSecond: 1, BurstSize: 1},
	})

	// First request: allowed
	if !rateLim.Allow("limited") {
		t.Error("first request should be allowed")
	}

	// Second request immediately: should be rejected (burst=1)
	if rateLim.Allow("limited") {
		t.Error("second immediate request should be rejected")
	}

	// Unlimted service: always allowed
	for i := 0; i < 100; i++ {
		if !rateLim.Allow("unlimited") {
			t.Error("unlimited service should always be allowed")
		}
	}
}

func TestHealthEndpoint(t *testing.T) {
	setupTestRouter(
		[]BackendConfig{{Name: "test", URL: "http://localhost:9999", Type: "vllm", MaxConcurrent: 10}},
		nil,
	)
	startupTime = time.Now()

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	handleHealth(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var health map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &health)

	if health["status"] != "ok" {
		t.Error("health status should be ok")
	}
	if health["service"] != "kronaxis-router" {
		t.Error("service name should be kronaxis-router")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	setupTestRouter(
		[]BackendConfig{{Name: "test", URL: "http://localhost:9999", Type: "vllm", MaxConcurrent: 10}},
		nil,
	)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rr := httptest.NewRecorder()
	handleMetrics(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	body, _ := io.ReadAll(rr.Body)
	content := string(body)

	if !strings.Contains(content, "kronaxis_router_requests_total") {
		t.Error("metrics should contain request counter")
	}
	if !strings.Contains(content, "kronaxis_router_backend_healthy") {
		t.Error("metrics should contain backend health gauge")
	}
	if !strings.Contains(content, "kronaxis_router_uptime_seconds") {
		t.Error("metrics should contain uptime gauge")
	}
}
