package main

import (
	"testing"
)

func TestCostTracker_Budget(t *testing.T) {
	ct := newCostTracker(map[string]BudgetConfig{
		"test": {DailyLimitUSD: 10.00, Action: "reject"},
	}, nil)

	// Under budget
	result := ct.checkBudget("test")
	if result.action != "" {
		t.Errorf("should be under budget, got action %q", result.action)
	}

	// Record costs to exceed budget
	ct.recordCost("test", 11.00)

	// Over budget
	result = ct.checkBudget("test")
	if result.action != "reject" {
		t.Errorf("should be over budget, expected reject, got %q", result.action)
	}
}

func TestCostTracker_DefaultBudget(t *testing.T) {
	ct := newCostTracker(map[string]BudgetConfig{
		"default": {DailyLimitUSD: 5.00, Action: "downgrade", DowngradeTarget: "cheap"},
	}, nil)

	ct.recordCost("unknown-service", 6.00)

	result := ct.checkBudget("unknown-service")
	if result.action != "downgrade" {
		t.Errorf("should fall back to default budget, got %q", result.action)
	}
	if result.downgradeTarget != "cheap" {
		t.Errorf("expected downgrade target 'cheap', got %q", result.downgradeTarget)
	}
}

func TestCostTracker_NoBudget(t *testing.T) {
	ct := newCostTracker(map[string]BudgetConfig{}, nil)

	result := ct.checkBudget("anything")
	if result.action != "" {
		t.Error("no budget should mean no action")
	}
}

func TestCostEstimate(t *testing.T) {
	ct := newCostTracker(nil, nil)
	backend := &Backend{Config: BackendConfig{CostInput1M: 0.15, CostOutput1M: 0.60}}

	cost := ct.estimateCost(backend, 1000, 500)
	expected := 0.15*1000/1_000_000 + 0.60*500/1_000_000
	if cost != expected {
		t.Errorf("expected %f, got %f", expected, cost)
	}
}
