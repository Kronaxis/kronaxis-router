package main

import (
	"testing"
)

func TestMatchRule_EmptyMatch(t *testing.T) {
	rule := &RoutingRule{Name: "catch-all", Match: RuleMatch{}}
	req := RouteRequest{Service: "test", CallType: "foo"}
	if !matchRule(rule, req) {
		t.Error("empty match should match everything")
	}
}

func TestMatchRule_ServiceMatch(t *testing.T) {
	rule := &RoutingRule{Name: "svc", Match: RuleMatch{Service: "service-a"}}

	if !matchRule(rule, RouteRequest{Service: "service-a"}) {
		t.Error("should match service-a")
	}
	if matchRule(rule, RouteRequest{Service: "service-b"}) {
		t.Error("should not match service-b")
	}
}

func TestMatchRule_TierMatch(t *testing.T) {
	rule := &RoutingRule{Name: "t2", Match: RuleMatch{Tier: 2}}

	if !matchRule(rule, RouteRequest{Tier: 2}) {
		t.Error("should match tier 2")
	}
	if matchRule(rule, RouteRequest{Tier: 1}) {
		t.Error("should not match tier 1")
	}
	if matchRule(rule, RouteRequest{Tier: 0}) {
		t.Error("should not match tier 0")
	}
}

func TestMatchRule_MultipleFields(t *testing.T) {
	rule := &RoutingRule{
		Name:  "specific",
		Match: RuleMatch{Service: "service-a", Tier: 2, Priority: "background"},
	}

	if !matchRule(rule, RouteRequest{Service: "service-a", Tier: 2, Priority: "background"}) {
		t.Error("should match all fields")
	}
	if matchRule(rule, RouteRequest{Service: "service-a", Tier: 2, Priority: "interactive"}) {
		t.Error("should not match wrong priority")
	}
	if matchRule(rule, RouteRequest{Service: "service-a", Tier: 1, Priority: "background"}) {
		t.Error("should not match wrong tier")
	}
}

func TestMatchRule_ContentType(t *testing.T) {
	rule := &RoutingRule{Name: "vision", Match: RuleMatch{ContentType: "vision"}}

	if !matchRule(rule, RouteRequest{ContentType: "vision"}) {
		t.Error("should match vision")
	}
	if matchRule(rule, RouteRequest{ContentType: "text"}) {
		t.Error("should not match text")
	}
}

func TestMatchRule_CaseInsensitive(t *testing.T) {
	rule := &RoutingRule{Name: "svc", Match: RuleMatch{Service: "Service-A"}}
	if !matchRule(rule, RouteRequest{Service: "service-a"}) {
		t.Error("should match case-insensitively")
	}
}

func TestRouteCandidates_PriorityOrder(t *testing.T) {
	bp := newBackendPool([]BackendConfig{
		{Name: "cheap", URL: "http://localhost:8001", Type: "vllm", CostOutput1M: 0.01, MaxConcurrent: 10},
		{Name: "expensive", URL: "http://localhost:8002", Type: "vllm", CostOutput1M: 1.00, MaxConcurrent: 10},
	})

	// Rules are evaluated in slice order; the higher-priority rule must come
	// first so it wins when multiple match. Within a rule, the order returned
	// by RouteCandidates is determined by the LCB tiebreaker (least active
	// connections, with round-robin between ties), not by the rule's declared
	// backend order -- so we assert on which Rule matched and which backends
	// are present, not on a specific candidate ordering.
	rules := []RoutingRule{
		{Name: "high", Priority: 200, Match: RuleMatch{Service: "vip"}, Backends: []string{"expensive"}},
		{Name: "low", Priority: 100, Match: RuleMatch{}, Backends: []string{"expensive", "cheap"}},
	}

	r := newRouter(rules, DefaultsConfig{FallbackChain: []string{"cheap"}}, bp)

	// Non-VIP should match the low-priority rule and see both backends.
	candidates := r.RouteCandidates(RouteRequest{Service: "normal"})
	if len(candidates) != 2 {
		t.Fatalf("non-VIP should have both backends, got %d", len(candidates))
	}
	if candidates[0].Rule == nil || candidates[0].Rule.Name != "low" {
		t.Errorf("non-VIP should match 'low' rule, got %v", candidates[0].Rule)
	}
	names := map[string]bool{}
	for _, c := range candidates {
		names[c.Backend.Config.Name] = true
	}
	if !names["expensive"] || !names["cheap"] {
		t.Errorf("non-VIP candidates should include both 'expensive' and 'cheap', got %v", names)
	}

	// VIP should match the high-priority rule and see only 'expensive'.
	candidates = r.RouteCandidates(RouteRequest{Service: "vip"})
	if len(candidates) != 1 {
		t.Fatalf("VIP should have exactly 1 candidate (expensive), got %d", len(candidates))
	}
	if candidates[0].Backend.Config.Name != "expensive" {
		t.Errorf("VIP should route to expensive, got %s", candidates[0].Backend.Config.Name)
	}
	if candidates[0].Rule == nil || candidates[0].Rule.Name != "high" {
		t.Errorf("VIP should match 'high' rule, got %v", candidates[0].Rule)
	}
}

func TestRouteCandidates_CostFilter(t *testing.T) {
	bp := newBackendPool([]BackendConfig{
		{Name: "cheap", URL: "http://localhost:8001", Type: "vllm", CostOutput1M: 0.01, MaxConcurrent: 10},
		{Name: "expensive", URL: "http://localhost:8002", Type: "vllm", CostOutput1M: 5.00, MaxConcurrent: 10},
	})

	rules := []RoutingRule{
		{Name: "budget", Priority: 100, Match: RuleMatch{}, Backends: []string{"expensive", "cheap"}, MaxCost: 0.50},
	}

	r := newRouter(rules, DefaultsConfig{}, bp)
	candidates := r.RouteCandidates(RouteRequest{})
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (cheap only), got %d", len(candidates))
	}
	if candidates[0].Backend.Config.Name != "cheap" {
		t.Errorf("should filter to cheap, got %s", candidates[0].Backend.Config.Name)
	}
}

func TestRouteCandidates_SkipsDownBackends(t *testing.T) {
	bp := newBackendPool([]BackendConfig{
		{Name: "down", URL: "http://localhost:8001", Type: "vllm", MaxConcurrent: 10},
		{Name: "up", URL: "http://localhost:8002", Type: "vllm", MaxConcurrent: 10},
	})

	// Mark one as down
	bp.backends["down"].Status = StatusDown

	rules := []RoutingRule{
		{Name: "test", Priority: 100, Match: RuleMatch{}, Backends: []string{"down", "up"}},
	}

	r := newRouter(rules, DefaultsConfig{}, bp)
	candidates := r.RouteCandidates(RouteRequest{})
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (up only), got %d", len(candidates))
	}
	if candidates[0].Backend.Config.Name != "up" {
		t.Errorf("should skip down backend, got %s", candidates[0].Backend.Config.Name)
	}
}

func TestRouteCandidates_FallbackChain(t *testing.T) {
	bp := newBackendPool([]BackendConfig{
		{Name: "a", URL: "http://localhost:8001", Type: "vllm", MaxConcurrent: 10},
		{Name: "b", URL: "http://localhost:8002", Type: "vllm", MaxConcurrent: 10},
	})

	// No rules match, should use fallback
	r := newRouter(nil, DefaultsConfig{FallbackChain: []string{"b", "a"}}, bp)
	candidates := r.RouteCandidates(RouteRequest{Service: "unknown"})
	if len(candidates) < 2 {
		t.Fatalf("expected 2 fallback candidates, got %d", len(candidates))
	}
	if candidates[0].Backend.Config.Name != "b" {
		t.Errorf("first fallback should be b, got %s", candidates[0].Backend.Config.Name)
	}
}

func TestResolveModelName_LoRA(t *testing.T) {
	backend := &Backend{Config: BackendConfig{
		Name: "test", ModelName: "base-model", LoRAAdapters: []string{"sdr", "closer"},
	}}

	// LoRA adapter requested and available
	name := resolveModelName("sdr", backend, "sdr")
	if name != "sdr" {
		t.Errorf("should use LoRA adapter name, got %s", name)
	}

	// No LoRA, use backend model
	name = resolveModelName("", backend, "")
	if name != "base-model" {
		t.Errorf("should use backend model name, got %s", name)
	}
}

func TestFilterByCost(t *testing.T) {
	backends := []*Backend{
		{Config: BackendConfig{Name: "cheap", CostInput1M: 0.01, CostOutput1M: 0.01}},
		{Config: BackendConfig{Name: "mid", CostInput1M: 0.10, CostOutput1M: 0.50}},
		{Config: BackendConfig{Name: "pricey", CostInput1M: 1.25, CostOutput1M: 10.0}},
	}

	filtered := filterByCost(backends, 0.50)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 within $0.50 ceiling, got %d", len(filtered))
	}
}
