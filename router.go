package main

import (
	"fmt"
	"strings"
	"sync"
)

// RouteRequest contains the metadata extracted from an incoming request.
type RouteRequest struct {
	Service     string // X-Kronaxis-Service header
	CallType    string // X-Kronaxis-CallType header
	Priority    string // X-Kronaxis-Priority header (interactive, normal, background, bulk)
	Tier        int    // X-Kronaxis-Tier header
	PersonaID   string // X-Kronaxis-PersonaID header
	ModelField  string // model field from OpenAI request body
	ContentType string // "text" or "vision" (detected from message content)
	Stream      bool   // stream field from OpenAI request body
}

// RouteResult is the outcome of routing: which backend to use and why.
type RouteResult struct {
	Backend   *Backend
	Rule      *RoutingRule // nil if default fallback was used
	ModelName string       // the model name to use in the forwarded request
}

// Router evaluates routing rules and selects backends.
type Router struct {
	rules    []RoutingRule
	defaults DefaultsConfig
	pool     *BackendPool
	mu       sync.RWMutex
}

func newRouter(rules []RoutingRule, defaults DefaultsConfig, pool *BackendPool) *Router {
	return &Router{
		rules:    rules,
		defaults: defaults,
		pool:     pool,
	}
}

func (r *Router) updateRules(rules []RoutingRule, defaults DefaultsConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = rules
	r.defaults = defaults
}

// Route selects the best backend for a given request.
// It evaluates rules in priority order (highest first), filters backends
// by health, capabilities, LoRA adapters, and cost constraints.
func (r *Router) Route(req RouteRequest) (RouteResult, error) {
	r.mu.RLock()
	rules := r.rules
	defaults := r.defaults
	r.mu.RUnlock()

	// Detect LoRA adapter request: if the model field matches a known adapter
	// on any backend, treat it as a LoRA routing request.
	loraAdapter := detectLoRAAdapter(req.ModelField, r.pool)

	// Try each rule in priority order
	for i := range rules {
		rule := &rules[i]
		if !matchRule(rule, req) {
			continue
		}

		backend := r.resolveBackend(rule, req, loraAdapter)
		if backend == nil {
			continue // Rule matched but no healthy backend available; try next rule
		}

		modelName := resolveModelName(req.ModelField, backend, loraAdapter)
		return RouteResult{
			Backend:   backend,
			Rule:      rule,
			ModelName: modelName,
		}, nil
	}

	// No rule matched: use default fallback chain
	backendNames := defaults.FallbackChain
	healthy := r.pool.GetHealthy(backendNames, nil)

	// If LoRA adapter requested, prefer backends with it
	if loraAdapter != "" {
		withAdapter := r.pool.GetWithAdapter(backendNames, loraAdapter)
		if len(withAdapter) > 0 {
			healthy = withAdapter
		}
	}

	if len(healthy) > 0 {
		backend := healthy[0]
		modelName := resolveModelName(req.ModelField, backend, loraAdapter)
		return RouteResult{
			Backend:   backend,
			ModelName: modelName,
		}, nil
	}

	return RouteResult{}, fmt.Errorf("no healthy backend available")
}

// matchRule checks whether a routing rule matches the incoming request.
// Only non-empty/non-zero fields in the rule must match.
func matchRule(rule *RoutingRule, req RouteRequest) bool {
	m := rule.Match

	if m.Service != "" && !strings.EqualFold(m.Service, req.Service) {
		return false
	}
	if m.CallType != "" && !strings.EqualFold(m.CallType, req.CallType) {
		return false
	}
	if m.Tier != 0 && m.Tier != req.Tier {
		return false
	}
	if m.Priority != "" && !strings.EqualFold(m.Priority, req.Priority) {
		return false
	}
	if m.ContentType != "" && !strings.EqualFold(m.ContentType, req.ContentType) {
		return false
	}
	if m.Model != "" && !strings.EqualFold(m.Model, req.ModelField) {
		return false
	}
	if m.LoRA != "" {
		adapter := detectLoRAAdapter(req.ModelField, pool)
		if !strings.EqualFold(m.LoRA, adapter) {
			return false
		}
	}

	return true
}

// resolveBackend finds the first healthy, capable backend from a rule's list.
func (r *Router) resolveBackend(rule *RoutingRule, req RouteRequest, loraAdapter string) *Backend {
	healthy := r.pool.GetHealthy(rule.Backends, rule.Required)

	// If LoRA adapter requested, prefer backends with it
	if loraAdapter != "" {
		withAdapter := filterByAdapter(healthy, loraAdapter)
		if len(withAdapter) > 0 {
			healthy = withAdapter
		}
		// If no backend has the adapter, fall back to any healthy backend
		// (system prompt will provide role context instead of LoRA)
	}

	// Apply cost ceiling
	if rule.MaxCost > 0 {
		healthy = filterByCost(healthy, rule.MaxCost)
	}

	if len(healthy) == 0 {
		return nil
	}
	return healthy[0]
}

// detectLoRAAdapter checks if the model field in the request corresponds to
// a LoRA adapter on any backend.
func detectLoRAAdapter(modelField string, pool *BackendPool) string {
	if modelField == "" || modelField == "default" {
		return ""
	}
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	for _, b := range pool.backends {
		for _, adapter := range b.Config.LoRAAdapters {
			if strings.EqualFold(adapter, modelField) {
				return adapter
			}
		}
	}
	return ""
}

// resolveModelName determines what model name to put in the forwarded request.
func resolveModelName(requestedModel string, backend *Backend, loraAdapter string) string {
	// If LoRA adapter was detected and backend has it, use adapter name
	if loraAdapter != "" && backend.HasAdapter(loraAdapter) {
		return loraAdapter
	}
	// If backend has a configured model name, use that
	if backend.Config.ModelName != "" {
		return backend.Config.ModelName
	}
	// Pass through the requested model name
	if requestedModel != "" {
		return requestedModel
	}
	return "default"
}

func filterByAdapter(backends []*Backend, adapter string) []*Backend {
	var result []*Backend
	for _, b := range backends {
		if b.HasAdapter(adapter) {
			result = append(result, b)
		}
	}
	return result
}

func filterByCost(backends []*Backend, maxCost float64) []*Backend {
	var result []*Backend
	for _, b := range backends {
		// Use the higher of input/output cost as the comparison
		cost := b.Config.CostInput1M
		if b.Config.CostOutput1M > cost {
			cost = b.Config.CostOutput1M
		}
		if cost <= maxCost {
			result = append(result, b)
		}
	}
	return result
}
