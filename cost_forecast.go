package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// CostForecaster predicts when a service's daily budget will be exhausted
// using linear extrapolation from current spend versus elapsed-time-of-day.
//
// It reads the existing CostTracker (costs.go) which already tallies
// per-service today-so-far spend. The forecaster does no I/O beyond a
// single read of that tracker; latency is microseconds.
type CostForecaster struct {
	tracker *CostTracker
}

// NewCostForecaster wraps an existing CostTracker.
func NewCostForecaster(tracker *CostTracker) *CostForecaster {
	return &CostForecaster{tracker: tracker}
}

// ForecastResult is one service's projected end-of-day spend and budget
// exhaustion ETA. Multiple ForecastResults are returned by the API.
type ForecastResult struct {
	Service                string  `json:"service"`
	BudgetUSD              float64 `json:"budget_usd"`
	SpentSoFarUSD          float64 `json:"spent_so_far_usd"`
	HoursElapsed           float64 `json:"hours_elapsed"`
	HoursRemaining         float64 `json:"hours_remaining"`
	ProjectedTotalUSD      float64 `json:"projected_total_usd"`
	ProjectedOverBudget    bool    `json:"projected_over_budget"`
	BudgetExhaustionTime   string  `json:"budget_exhaustion_time,omitempty"` // RFC3339 if extrapolated to hit budget today
	BurnRateUSDPerHour     float64 `json:"burn_rate_usd_per_hour"`
}

// Forecast computes one ForecastResult per configured service+budget.
// Services with no budget configured are skipped.
//
// Algorithm (deliberately simple to remain interpretable):
//  - hours_elapsed = (time-of-day in UTC) / 1h
//  - burn_rate = spent_so_far / hours_elapsed (zero before 0.1 h)
//  - projected_total = burn_rate × 24
//  - exhaustion_time = now + (budget - spent_so_far) / burn_rate (when burn rate > 0)
//
// Linear extrapolation is right for steady workloads. For bursty
// workloads it overestimates early in the day and undercounts late;
// callers who care can tighten with EWMA in a follow-up.
func (f *CostForecaster) Forecast() []ForecastResult {
	if f == nil || f.tracker == nil || cfg == nil {
		return []ForecastResult{}
	}
	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	hoursElapsed := now.Sub(startOfDay).Hours()
	hoursRemaining := 24 - hoursElapsed
	out := []ForecastResult{}
	for service, budget := range cfg.Budgets {
		spent := f.tracker.spentToday(service)
		burn := 0.0
		if hoursElapsed > 0.1 {
			burn = spent / hoursElapsed
		}
		projected := burn * 24
		over := projected > budget.DailyLimitUSD && budget.DailyLimitUSD > 0
		exhaustion := ""
		if burn > 0 && budget.DailyLimitUSD > 0 && spent < budget.DailyLimitUSD {
			hoursToBudget := (budget.DailyLimitUSD - spent) / burn
			if hoursToBudget < hoursRemaining {
				exhaustion = now.Add(time.Duration(hoursToBudget * float64(time.Hour))).Format(time.RFC3339)
			}
		}
		out = append(out, ForecastResult{
			Service:              service,
			BudgetUSD:            budget.DailyLimitUSD,
			SpentSoFarUSD:        spent,
			HoursElapsed:         hoursElapsed,
			HoursRemaining:       hoursRemaining,
			BurnRateUSDPerHour:   burn,
			ProjectedTotalUSD:    projected,
			ProjectedOverBudget:  over,
			BudgetExhaustionTime: exhaustion,
		})
	}
	return out
}

// handleCostForecast serves GET /api/costs/forecast.
func handleCostForecast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if costForecaster == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []any{},
			"note":   "cost forecaster not initialised; check that budgets are configured and a CostTracker is wired",
		})
		return
	}
	results := costForecaster.Forecast()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object":      "list",
		"as_of":       time.Now().UTC().Format(time.RFC3339),
		"data":        results,
	})
}

// costForecaster is the package-level handle. Set in main.go after
// CostTracker is initialised.
var costForecaster *CostForecaster

// initCostForecaster wires the forecaster to the CostTracker. Safe to
// call multiple times; latest tracker wins.
func initCostForecaster() {
	if costs == nil {
		return
	}
	costForecaster = NewCostForecaster(costs)
}

// counterfactualCost computes "what would this request have cost on
// every other healthy backend?" for the per-request comparison view.
// Returns a map of backend-name → estimated USD cost using cost_input_1m
// and cost_output_1m from each backend's config.
//
// Used by /api/costs/counterfactual?request_id=X (not implemented in
// this layer; it would need a per-request cost log, which is the job
// of a future migration).
func counterfactualCost(inputTokens, outputTokens int) map[string]float64 {
	if pool == nil {
		return nil
	}
	out := map[string]float64{}
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	for name, b := range pool.backends {
		ci := b.Config.CostInput1M
		co := b.Config.CostOutput1M
		usd := (ci*float64(inputTokens) + co*float64(outputTokens)) / 1e6
		out[name] = usd
	}
	return out
}

// stub placeholder so context import isn't dropped; future wiring will
// need ctx for shadow routing's per-request goroutine cancellation.
var _ = context.Background
