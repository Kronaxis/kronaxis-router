package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// CostTracker manages per-service cost tracking and budget enforcement.
type CostTracker struct {
	budgets    map[string]BudgetConfig
	dailyCosts map[string]float64
	resetDate  string // YYYY-MM-DD
	db         *sql.DB
	logSem     chan struct{} // bounded concurrency for DB writes
	mu         sync.RWMutex
}

type budgetCheck struct {
	action          string // "", "reject", "downgrade"
	downgradeTarget string
}

func newCostTracker(budgets map[string]BudgetConfig, db *sql.DB) *CostTracker {
	ct := &CostTracker{
		budgets:    budgets,
		dailyCosts: make(map[string]float64),
		resetDate:  time.Now().Format("2006-01-02"),
		db:         db,
		logSem:     make(chan struct{}, 50),
	}
	// Seed daily costs from DB if available
	if db != nil {
		ct.seedFromDB()
	}
	return ct
}

func (ct *CostTracker) updateBudgets(budgets map[string]BudgetConfig) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.budgets = budgets
}

// checkBudget evaluates whether a service has exceeded its daily budget.
func (ct *CostTracker) checkBudget(service string) budgetCheck {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// Reset on day change
	today := time.Now().Format("2006-01-02")
	if today != ct.resetDate {
		ct.dailyCosts = make(map[string]float64)
		ct.resetDate = today
	}

	// Look up budget: service-specific, then "default"
	budget, ok := ct.budgets[service]
	if !ok {
		budget, ok = ct.budgets["default"]
	}
	if !ok {
		return budgetCheck{} // No budget configured
	}

	spent := ct.dailyCosts[service]
	if spent >= budget.DailyLimitUSD {
		return budgetCheck{
			action:          budget.Action,
			downgradeTarget: budget.DowngradeTarget,
		}
	}

	return budgetCheck{}
}

// recordCost adds a cost to the daily tracker and logs to the database.
func (ct *CostTracker) recordCost(service string, cost float64) {
	ct.mu.Lock()
	ct.dailyCosts[service] += cost
	ct.mu.Unlock()
}

func (ct *CostTracker) estimateCost(backend *Backend, inputTokens, outputTokens int) float64 {
	inputCost := float64(inputTokens) * backend.Config.CostInput1M / 1_000_000
	outputCost := float64(outputTokens) * backend.Config.CostOutput1M / 1_000_000
	return inputCost + outputCost
}

// logToDB writes a call record to llm_call_log asynchronously.
func (ct *CostTracker) logToDB(
	service string,
	tier int,
	callType, personaID, model, provider string,
	inputTokens, outputTokens int,
	estimatedCost float64,
	latencyMS int64,
	success bool,
	errMsg string,
) {
	if ct.db == nil {
		return
	}

	select {
	case ct.logSem <- struct{}{}:
	default:
		logger.Println("cost log semaphore full, skipping")
		return
	}

	go func() {
		defer func() { <-ct.logSem }()

		_, err := ct.db.Exec(`
			INSERT INTO llm_call_log
				(tier, call_type, persona_id, input_text, output_text,
				 latency_ms, success, error_msg, provider, model,
				 input_tokens, output_tokens, estimated_cost, service)
			VALUES ($1, $2, $3, '', '', $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			tier, callType, nullableString(personaID),
			latencyMS, success, nullableString(errMsg),
			provider, model,
			inputTokens, outputTokens, estimatedCost,
			service,
		)
		if err != nil {
			logger.Printf("cost log error: %v", err)
		}
	}()
}

func (ct *CostTracker) seedFromDB() {
	today := time.Now().Format("2006-01-02")
	rows, err := ct.db.Query(`
		SELECT COALESCE(service, 'unknown'), COALESCE(SUM(estimated_cost), 0)
		FROM llm_call_log
		WHERE created_at >= $1::date
		GROUP BY service`, today)
	if err != nil {
		logger.Printf("failed to seed daily costs: %v", err)
		return
	}
	defer rows.Close()

	ct.mu.Lock()
	defer ct.mu.Unlock()
	for rows.Next() {
		var svc string
		var cost float64
		if err := rows.Scan(&svc, &cost); err == nil {
			ct.dailyCosts[svc] = cost
		}
	}
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// logRequest logs a completed request to the cost tracker and database.
func logRequest(meta RouteRequest, route RouteResult, inputTokens, outputTokens int, latency time.Duration, success bool, errMsg string) {
	if route.Backend == nil {
		return
	}

	cost := costs.estimateCost(route.Backend, inputTokens, outputTokens)
	costs.recordCost(meta.Service, cost)

	provider := route.Backend.Config.Type
	model := route.ModelName

	costs.logToDB(
		meta.Service, meta.Tier, meta.CallType, meta.PersonaID,
		model, provider,
		inputTokens, outputTokens, cost,
		latency.Milliseconds(),
		success, errMsg,
	)
}

// handleCosts serves the cost dashboard API.
func handleCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	costs.mu.RLock()
	daily := make(map[string]float64)
	for k, v := range costs.dailyCosts {
		daily[k] = v
	}
	budgets := make(map[string]BudgetConfig)
	for k, v := range costs.budgets {
		budgets[k] = v
	}
	costs.mu.RUnlock()

	result := map[string]interface{}{
		"date":    time.Now().Format("2006-01-02"),
		"daily":   daily,
		"budgets": budgets,
	}

	// If DB is available, add breakdown by model and call type
	if db != nil {
		period := r.URL.Query().Get("period")
		if period == "" {
			period = "today"
		}
		breakdown := getCostBreakdown(period)
		if breakdown != nil {
			result["breakdown"] = breakdown
		}
	}

	writeJSON(w, 200, result)
}

func getCostBreakdown(period string) []map[string]interface{} {
	var dateFilter string
	switch period {
	case "today":
		dateFilter = "created_at >= CURRENT_DATE"
	case "week":
		dateFilter = "created_at >= CURRENT_DATE - INTERVAL '7 days'"
	case "month":
		dateFilter = "created_at >= CURRENT_DATE - INTERVAL '30 days'"
	default:
		dateFilter = "created_at >= CURRENT_DATE"
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(service, 'unknown') as service,
			COALESCE(model, 'unknown') as model,
			COALESCE(call_type, 'unknown') as call_type,
			COUNT(*) as request_count,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(estimated_cost), 0) as total_cost,
			COALESCE(AVG(latency_ms), 0) as avg_latency_ms
		FROM llm_call_log
		WHERE %s
		GROUP BY service, model, call_type
		ORDER BY total_cost DESC
		LIMIT 100`, dateFilter)

	rows, err := db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var service, model, callType string
		var reqCount, inputTokens, outputTokens int64
		var totalCost, avgLatency float64

		if err := rows.Scan(&service, &model, &callType, &reqCount,
			&inputTokens, &outputTokens, &totalCost, &avgLatency); err != nil {
			continue
		}
		result = append(result, map[string]interface{}{
			"service":            service,
			"model":              model,
			"call_type":          callType,
			"request_count":      reqCount,
			"total_input_tokens": inputTokens,
			"total_output_tokens": outputTokens,
			"total_cost_usd":     totalCost,
			"avg_latency_ms":     avgLatency,
		})
	}
	return result
}

// handleBackends handles dynamic backend registration.
func handleBackends(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, pool.allStatuses())

	case http.MethodPost:
		var cfg BackendConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeErrorJSON(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if cfg.Name == "" || cfg.URL == "" {
			writeErrorJSON(w, 400, "name and url are required")
			return
		}
		// SSRF prevention: validate URL is not targeting internal networks
		if err := ValidateExternalURL(cfg.URL); err != nil {
			// Allow private IPs only if ROUTER_ALLOW_PRIVATE_BACKENDS is set
			if env("ROUTER_ALLOW_PRIVATE_BACKENDS", "") != "true" {
				writeErrorJSON(w, 400, "invalid backend URL: "+err.Error())
				return
			}
		}
		cfg.Dynamic = true
		pool.Register(cfg)
		logger.Printf("dynamic backend registered: %s -> %s", cfg.Name, cfg.URL)
		writeJSON(w, 200, map[string]string{"status": "registered", "name": cfg.Name})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			writeErrorJSON(w, 400, "name parameter required")
			return
		}
		pool.Deregister(name)
		logger.Printf("backend deregistered: %s", name)
		writeJSON(w, 200, map[string]string{"status": "deregistered", "name": name})

	default:
		http.Error(w, "method not allowed", 405)
	}
}
