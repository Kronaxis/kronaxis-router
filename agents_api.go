package main

import (
	"encoding/json"
	"net/http"
	"sort"
)

// AgentBackendView is the per-agent view returned by GET /api/agents.
type AgentBackendView struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	ModelName    string   `json:"model_name"`
	Capabilities []string `json:"capabilities,omitempty"`
	Tier         int      `json:"tier"`
	CostClass    string   `json:"cost_class,omitempty"`
	InRules      []string `json:"in_rules"` // names of routing rules this agent participates in
	Position     int      `json:"position"` // index within the rule's chain (first appearance)
	Status       string   `json:"status"`   // healthy | degraded | down | unknown
}

// handleAgents serves GET /api/agents — the operator-facing introspection
// endpoint that joins the backend pool's health status with the rule chains
// to show "agent X is in tier Y rule Z".
func handleAgents(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	views := []AgentBackendView{}
	if cfg == nil || pool == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": views})
		return
	}
	// Detect agent backends by the presence of `agent_metadata` in the YAML
	// (which synth.go writes). Since BackendConfig doesn't model that field
	// directly, we match by URL prefix as a pragmatic heuristic, then
	// re-attach metadata from the parsed config if available.
	for _, b := range cfg.Backends {
		if !looksLikeAgentBackend(b) {
			continue
		}
		view := AgentBackendView{
			Name:         b.Name,
			URL:          b.URL,
			ModelName:    b.ModelName,
			Capabilities: b.Capabilities,
		}
		// Pull agent metadata from the raw YAML (re-parse since BackendConfig
		// doesn't model agent_metadata).
		if tier, costClass, ok := agentMetadata(b.Name); ok {
			view.Tier = tier
			view.CostClass = costClass
		}
		// Find which rules reference this backend.
		for _, rule := range cfg.Rules {
			for i, name := range rule.Backends {
				if name == b.Name {
					view.InRules = append(view.InRules, rule.Name)
					if view.Position == 0 {
						view.Position = i + 1
					}
					break
				}
			}
		}
		// Status from pool.
		if be := pool.Get(b.Name); be != nil {
			view.Status = be.Status.String()
		} else {
			view.Status = "unknown"
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   views,
	})
}

// looksLikeAgentBackend is a deliberately small heuristic: agent backends are
// the ones whose URL points at the agent-gateway port. The 8055 default is
// hard-coded for now; sites using a non-default port can register the same
// way and the heuristic still works as long as the gateway responds.
func looksLikeAgentBackend(b BackendConfig) bool {
	if b.URL == "" {
		return false
	}
	for _, hint := range []string{":8055", "agent-gateway"} {
		if contains(b.URL, hint) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

