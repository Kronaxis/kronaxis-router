package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// RouteRequest contains the metadata extracted from an incoming request.
type RouteRequest struct {
	Service         string          // X-Kronaxis-Service header
	CallType        string          // X-Kronaxis-CallType header
	Priority        string          // X-Kronaxis-Priority header (interactive, normal, background, bulk)
	Tier            int             // X-Kronaxis-Tier header (0=auto, 1=heavy, 2=light)
	PersonaID       string          // X-Kronaxis-PersonaID header
	ModelField      string          // model field from OpenAI request body
	ContentType     string          // "text" or "vision" (detected from message content)
	Stream          bool            // stream field from OpenAI request body
	ComplexityScore ComplexityScore // 0-100 auto-classified complexity
}

// RouteResult is the outcome of routing: which backend to use and why.
type RouteResult struct {
	Backend    *Backend
	Rule       *RoutingRule    // nil if default fallback was used
	ModelName  string          // the model name to use in the forwarded request
	Complexity ComplexityScore // 0-100 complexity score for this request
}

// Router evaluates routing rules and selects backends.
type Router struct {
	rules    []RoutingRule
	defaults DefaultsConfig
	pool     *BackendPool
	mu       sync.RWMutex
	rrCount  atomic.Uint64 // round-robin counter for load balancing
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
// Uses least-connections with round-robin tiebreaker: when multiple backends
// have equal active request counts, rotates between them.
func (r *Router) Route(req RouteRequest) (RouteResult, error) {
	candidates := r.RouteCandidates(req)
	if len(candidates) == 0 {
		return RouteResult{}, fmt.Errorf("no healthy backend available")
	}
	if len(candidates) > 1 {
		sort.Slice(candidates, func(i, j int) bool {
			ai := candidates[i].Backend.ActiveReqs.Load()
			aj := candidates[j].Backend.ActiveReqs.Load()
			return ai < aj
		})
		// Round-robin tiebreaker: when least-busy candidates are tied,
		// rotate which one is picked to distribute load evenly.
		minLoad := candidates[0].Backend.ActiveReqs.Load()
		tied := 1
		for tied < len(candidates) && candidates[tied].Backend.ActiveReqs.Load() == minLoad {
			tied++
		}
		if tied > 1 {
			pick := int(r.rrCount.Add(1)-1) % tied
			candidates[0], candidates[pick] = candidates[pick], candidates[0]
		}
	}
	return candidates[0], nil
}

// RouteCandidates returns all viable backends for a request.
// Sorted by least-connections with round-robin tiebreaker for even load distribution.
func (r *Router) RouteCandidates(req RouteRequest) []RouteResult {
	r.mu.RLock()
	rules := r.rules
	defaults := r.defaults
	r.mu.RUnlock()

	loraAdapter := detectLoRAAdapter(req.ModelField, r.pool)

	// Try each rule in priority order
	for i := range rules {
		rule := &rules[i]
		if !matchRule(rule, req) {
			continue
		}

		candidates := r.resolveAllBackends(rule, req, loraAdapter)
		if len(candidates) == 0 {
			continue
		}
		return r.balanceCandidates(candidates)
	}

	// No rule matched: use default fallback chain
	backendNames := defaults.FallbackChain
	healthy := r.pool.GetHealthy(backendNames, nil)

	if loraAdapter != "" {
		withAdapter := r.pool.GetWithAdapter(backendNames, loraAdapter)
		if len(withAdapter) > 0 {
			healthy = withAdapter
		}
	}

	var results []RouteResult
	for _, backend := range healthy {
		results = append(results, RouteResult{
			Backend:    backend,
			ModelName:  resolveModelName(req.ModelField, backend, loraAdapter),
			Complexity: req.ComplexityScore,
		})
	}
	return r.balanceCandidates(results)
}

// balanceCandidates sorts by least-connections with round-robin tiebreaker.
func (r *Router) balanceCandidates(candidates []RouteResult) []RouteResult {
	if len(candidates) <= 1 {
		return candidates
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Backend.ActiveReqs.Load() < candidates[j].Backend.ActiveReqs.Load()
	})
	minLoad := candidates[0].Backend.ActiveReqs.Load()
	tied := 1
	for tied < len(candidates) && candidates[tied].Backend.ActiveReqs.Load() == minLoad {
		tied++
	}
	if tied > 1 {
		pick := int(r.rrCount.Add(1)-1) % tied
		candidates[0], candidates[pick] = candidates[pick], candidates[0]
	}
	return candidates
}

// resolveAllBackends returns all healthy, capable backends from a rule's list.
func (r *Router) resolveAllBackends(rule *RoutingRule, req RouteRequest, loraAdapter string) []RouteResult {
	healthy := r.pool.GetHealthy(rule.Backends, rule.Required)

	if loraAdapter != "" {
		withAdapter := filterByAdapter(healthy, loraAdapter)
		if len(withAdapter) > 0 {
			healthy = withAdapter
		}
	}

	if rule.MaxCost > 0 {
		healthy = filterByCost(healthy, rule.MaxCost)
	}

	var results []RouteResult
	for _, backend := range healthy {
		results = append(results, RouteResult{
			Backend:    backend,
			Rule:       rule,
			ModelName:  resolveModelName(req.ModelField, backend, loraAdapter),
			Complexity: req.ComplexityScore,
		})
	}
	return results
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
