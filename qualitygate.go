package main

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// QualityGate checks responses before returning them to the caller.
// If the cheap model's output fails the quality check, the request is
// automatically retried on a more capable backend. The caller never
// sees the bad response.
//
// Two modes:
//   Mode A (default): Sequential gate. Send to cheap model, check response,
//     retry on expensive model if check fails. Adds one round-trip on failures.
//   Mode B: Parallel dispatch. Send to cheap AND expensive model simultaneously.
//     Return cheap response if it passes, expensive response if it fails.
//     Cancel the unused request. Costs 2x tokens on gated requests but adds
//     zero latency on failures.

type QualityGateConfig struct {
	Enabled    bool    `yaml:"enabled" json:"enabled"`
	Mode       string  `yaml:"mode" json:"mode"`               // "sequential" (default) or "parallel"
	SampleRate float64 `yaml:"sample_rate" json:"sample_rate"`  // 0.0-1.0, fraction of requests to gate (1.0 = all)
	FallbackBackend string `yaml:"fallback_backend" json:"fallback_backend"` // backend name for retry/parallel
	Checks     GateChecks `yaml:"checks" json:"checks"`
}

// GateChecks defines what quality checks to run on the response.
type GateChecks struct {
	MinLength      int  `yaml:"min_length" json:"min_length"`            // response must be >= N chars
	MaxEmptyRate   float64 `yaml:"max_empty_rate" json:"max_empty_rate"` // reject if empty content
	ValidJSON      bool `yaml:"valid_json" json:"valid_json"`            // if system prompt asks for JSON, validate it parses
	NoRefusal      bool `yaml:"no_refusal" json:"no_refusal"`            // reject "I can't help with that" responses
	MinTokens      int  `yaml:"min_tokens" json:"min_tokens"`            // response must have >= N tokens
}

type QualityGate struct {
	config    QualityGateConfig
	gated     atomic.Int64
	passed    atomic.Int64
	retried   atomic.Int64
	mu        sync.RWMutex
}

func newQualityGate(config QualityGateConfig) *QualityGate {
	if config.Mode == "" {
		config.Mode = "sequential"
	}
	return &QualityGate{config: config}
}

func (qg *QualityGate) updateConfig(config QualityGateConfig) {
	qg.mu.Lock()
	defer qg.mu.Unlock()
	if config.Mode == "" {
		config.Mode = "sequential"
	}
	qg.config = config
}

// ShouldGate returns true if this request should be quality-gated.
func (qg *QualityGate) ShouldGate(meta RouteRequest) bool {
	qg.mu.RLock()
	defer qg.mu.RUnlock()

	if !qg.config.Enabled {
		return false
	}
	// Don't gate interactive or streaming requests (latency-sensitive)
	if meta.Priority == "interactive" || meta.Stream {
		return false
	}
	// Gate based on sample rate (1.0 = gate everything)
	if qg.config.SampleRate >= 1.0 {
		return true
	}
	return false // For sample rates < 1.0, handled by caller
}

// GateSequential implements Mode A: send to cheap, check, retry if bad.
// Returns the final response (either from cheap model or fallback).
func (qg *QualityGate) GateSequential(
	cheapResponse []byte,
	cheapStatus int,
	req *ChatRequest,
	meta RouteRequest,
	systemPrompt string,
) ([]byte, int, bool) {
	qg.gated.Add(1)

	// Run quality checks on the cheap response
	if cheapStatus >= 400 {
		// HTTP error from cheap model: always retry
		return qg.retryOnFallback(req, meta)
	}

	var resp ChatResponse
	if err := json.Unmarshal(cheapResponse, &resp); err != nil {
		return qg.retryOnFallback(req, meta)
	}

	content := ""
	if len(resp.Choices) > 0 {
		if s, ok := resp.Choices[0].Message.Content.(string); ok {
			content = s
		}
	}

	if qg.passesChecks(content, systemPrompt) {
		qg.passed.Add(1)
		return cheapResponse, cheapStatus, false // Pass: return cheap response
	}

	// Failed checks: retry on fallback
	return qg.retryOnFallback(req, meta)
}

// GateParallel implements Mode B: send to both, return the better one.
// Returns (response, statusCode, usedFallback).
func (qg *QualityGate) GateParallel(
	req *ChatRequest,
	meta RouteRequest,
	cheapBackend *Backend,
	cheapModelName string,
	body []byte,
	systemPrompt string,
) ([]byte, int, bool) {
	qg.gated.Add(1)

	qg.mu.RLock()
	fallbackName := qg.config.FallbackBackend
	qg.mu.RUnlock()

	fallback := pool.Get(fallbackName)
	if fallback == nil || !fallback.IsAvailable() {
		// No fallback: just use cheap model
		statusCode, _, respBody, err := forwardToBackend(cheapBackend, cheapModelName, body, req, meta)
		if err != nil {
			return nil, 502, false
		}
		qg.passed.Add(1)
		return respBody, statusCode, false
	}

	// Dispatch to both backends simultaneously
	type result struct {
		body    []byte
		status  int
		err     error
		backend string
	}

	results := make(chan result, 2)

	// Cheap model
	go func() {
		statusCode, _, respBody, err := forwardToBackend(cheapBackend, cheapModelName, body, req, meta)
		results <- result{respBody, statusCode, err, "cheap"}
	}()

	// Expensive model
	fallbackReq := *req
	fallbackReq.Model = fallback.Config.ModelName
	injectQwenThinkingDisabled(&fallbackReq, fallback)
	fallbackBody, _ := json.Marshal(fallbackReq)

	go func() {
		statusCode, _, respBody, err := forwardToBackend(fallback, fallback.Config.ModelName, fallbackBody, &fallbackReq, meta)
		results <- result{respBody, statusCode, err, "fallback"}
	}()

	// Collect both results
	var cheapResult, fallbackResult result
	for i := 0; i < 2; i++ {
		r := <-results
		if r.backend == "cheap" {
			cheapResult = r
		} else {
			fallbackResult = r
		}
	}

	// If cheap model passed quality checks, use it (cheaper)
	if cheapResult.err == nil && cheapResult.status < 400 {
		content := extractContent(cheapResult.body)
		if qg.passesChecks(content, systemPrompt) {
			qg.passed.Add(1)
			return cheapResult.body, cheapResult.status, false
		}
	}

	// Cheap model failed: use fallback
	if fallbackResult.err == nil && fallbackResult.status < 400 {
		qg.retried.Add(1)
		logger.Printf("[quality-gate] parallel: cheap model failed checks, using fallback")
		return fallbackResult.body, fallbackResult.status, true
	}

	// Both failed: return whatever we have
	if cheapResult.err == nil {
		return cheapResult.body, cheapResult.status, false
	}
	return fallbackResult.body, fallbackResult.status, true
}

// passesChecks runs all configured quality checks on the response content.
func (qg *QualityGate) passesChecks(content, systemPrompt string) bool {
	qg.mu.RLock()
	checks := qg.config.Checks
	qg.mu.RUnlock()

	// Empty response check
	if checks.MaxEmptyRate >= 0 && len(strings.TrimSpace(content)) == 0 {
		return false
	}

	// Minimum length
	if checks.MinLength > 0 && len(content) < checks.MinLength {
		return false
	}

	// Minimum tokens
	if checks.MinTokens > 0 && CountTokens(content) < checks.MinTokens {
		return false
	}

	// JSON validation (if the system prompt asks for JSON)
	if checks.ValidJSON {
		sysLower := strings.ToLower(systemPrompt)
		if strings.Contains(sysLower, "json") ||
			strings.Contains(sysLower, "respond only in json") ||
			strings.Contains(sysLower, "return json") {
			trimmed := strings.TrimSpace(content)
			// Strip markdown code fences
			trimmed = strings.TrimPrefix(trimmed, "```json")
			trimmed = strings.TrimPrefix(trimmed, "```")
			trimmed = strings.TrimSuffix(trimmed, "```")
			trimmed = strings.TrimSpace(trimmed)
			if !json.Valid([]byte(trimmed)) {
				return false
			}
		}
	}

	// Refusal detection
	if checks.NoRefusal {
		lower := strings.ToLower(content)
		refusalPatterns := []string{
			"i can't help",
			"i cannot help",
			"i'm not able to",
			"i am not able to",
			"as an ai",
			"i don't have the ability",
			"i'm unable to",
			"i must decline",
			"i cannot assist",
			"against my guidelines",
		}
		for _, pattern := range refusalPatterns {
			if strings.Contains(lower, pattern) {
				return false
			}
		}
	}

	return true
}

// retryOnFallback sends the request to the configured fallback backend.
func (qg *QualityGate) retryOnFallback(req *ChatRequest, meta RouteRequest) ([]byte, int, bool) {
	qg.mu.RLock()
	fallbackName := qg.config.FallbackBackend
	qg.mu.RUnlock()

	fallback := pool.Get(fallbackName)
	if fallback == nil || !fallback.IsAvailable() {
		qg.retried.Add(1)
		return nil, 503, true
	}

	fallbackReq := *req
	fallbackReq.Model = fallback.Config.ModelName
	injectQwenThinkingDisabled(&fallbackReq, fallback)
	body, _ := json.Marshal(fallbackReq)

	statusCode, _, respBody, err := forwardToBackend(fallback, fallback.Config.ModelName, body, &fallbackReq, meta)
	if err != nil {
		return nil, 502, true
	}

	qg.retried.Add(1)
	logger.Printf("[quality-gate] sequential: retried on %s (cheap model failed checks)", fallbackName)
	return respBody, statusCode, true
}

func extractContent(body []byte) string {
	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if len(resp.Choices) == 0 {
		return ""
	}
	if s, ok := resp.Choices[0].Message.Content.(string); ok {
		return s
	}
	return ""
}

// Stats returns quality gate statistics.
func (qg *QualityGate) Stats() map[string]interface{} {
	qg.mu.RLock()
	defer qg.mu.RUnlock()

	total := qg.gated.Load()
	passed := qg.passed.Load()
	retried := qg.retried.Load()
	retryRate := float64(0)
	if total > 0 {
		retryRate = float64(retried) / float64(total) * 100
	}

	return map[string]interface{}{
		"enabled":    qg.config.Enabled,
		"mode":       qg.config.Mode,
		"gated":      total,
		"passed":     passed,
		"retried":    retried,
		"retry_rate": retryRate,
		"fallback":   qg.config.FallbackBackend,
	}
}

// Prometheus metrics for quality gate
func (qg *QualityGate) recordMetrics(usedFallback bool, latency time.Duration) {
	if usedFallback {
		prom.getCounter(prom.requestsTotal, "quality_gate_retry").Add(1)
	}
}
