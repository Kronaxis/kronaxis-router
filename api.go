package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

// handleRules serves CRUD for routing rules.
func handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		configMu.RLock()
		rules := cfg.Rules
		configMu.RUnlock()
		writeJSON(w, 200, rules)

	case http.MethodPost:
		var rule RoutingRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeErrorJSON(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if rule.Name == "" {
			writeErrorJSON(w, 400, "rule name is required")
			return
		}

		configMu.Lock()
		cfg.Rules = append(cfg.Rules, rule)
		sort.Slice(cfg.Rules, func(i, j int) bool {
			return cfg.Rules[i].Priority > cfg.Rules[j].Priority
		})
		rtr.updateRules(cfg.Rules, cfg.Defaults)
		configMu.Unlock()

		saveConfigToDisk()
		writeJSON(w, 201, rule)

	case http.MethodPut:
		var rule RoutingRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeErrorJSON(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if rule.Name == "" {
			writeErrorJSON(w, 400, "rule name is required")
			return
		}

		configMu.Lock()
		found := false
		for i, r := range cfg.Rules {
			if r.Name == rule.Name {
				cfg.Rules[i] = rule
				found = true
				break
			}
		}
		if !found {
			cfg.Rules = append(cfg.Rules, rule)
		}
		sort.Slice(cfg.Rules, func(i, j int) bool {
			return cfg.Rules[i].Priority > cfg.Rules[j].Priority
		})
		rtr.updateRules(cfg.Rules, cfg.Defaults)
		configMu.Unlock()

		saveConfigToDisk()
		writeJSON(w, 200, rule)

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			writeErrorJSON(w, 400, "name parameter required")
			return
		}

		configMu.Lock()
		for i, r := range cfg.Rules {
			if r.Name == name {
				cfg.Rules = append(cfg.Rules[:i], cfg.Rules[i+1:]...)
				break
			}
		}
		rtr.updateRules(cfg.Rules, cfg.Defaults)
		configMu.Unlock()

		saveConfigToDisk()
		writeJSON(w, 200, map[string]string{"status": "deleted", "name": name})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleBudgets serves CRUD for per-service budgets.
func handleBudgets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		configMu.RLock()
		budgets := cfg.Budgets
		configMu.RUnlock()
		writeJSON(w, 200, budgets)

	case http.MethodPut:
		var budgets map[string]BudgetConfig
		if err := json.NewDecoder(r.Body).Decode(&budgets); err != nil {
			writeErrorJSON(w, 400, "invalid JSON: "+err.Error())
			return
		}

		configMu.Lock()
		cfg.Budgets = budgets
		costs.updateBudgets(budgets)
		configMu.Unlock()

		saveConfigToDisk()
		writeJSON(w, 200, budgets)

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleConfigYAML serves raw YAML get/put.
func handleConfigYAML(w http.ResponseWriter, r *http.Request) {
	configPath := env("CONFIG_PATH", "config.yaml")

	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(configPath)
		if err != nil {
			writeErrorJSON(w, 500, "failed to read config: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		w.WriteHeader(200)
		w.Write(data)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeErrorJSON(w, 400, "failed to read body")
			return
		}

		// Validate before saving
		_, err = loadConfigFromBytes(body)
		if err != nil {
			writeErrorJSON(w, 400, "invalid config: "+err.Error())
			return
		}

		if err := os.WriteFile(configPath, body, 0644); err != nil {
			writeErrorJSON(w, 500, "failed to write config: "+err.Error())
			return
		}

		// Force reload
		newCfg, _ := loadConfig(configPath)
		configMu.Lock()
		cfg = newCfg
		pool.updateBackends(newCfg.Backends)
		rtr.updateRules(newCfg.Rules, newCfg.Defaults)
		bat.updateConfig(newCfg.Batching)
		costs.updateBudgets(newCfg.Budgets)
		configMu.Unlock()

		logger.Println("config updated via API")
		writeJSON(w, 200, map[string]string{"status": "updated"})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleConfigReload forces a config reload from disk.
func handleConfigReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	configPath := env("CONFIG_PATH", "config.yaml")
	newCfg, err := loadConfig(configPath)
	if err != nil {
		writeErrorJSON(w, 500, "reload failed: "+err.Error())
		return
	}

	configMu.Lock()
	cfg = newCfg
	pool.updateBackends(newCfg.Backends)
	rtr.updateRules(newCfg.Rules, newCfg.Defaults)
	bat.updateConfig(newCfg.Batching)
	costs.updateBudgets(newCfg.Budgets)
	configMu.Unlock()

	logger.Println("config reloaded via API")
	writeJSON(w, 200, map[string]string{
		"status":   "reloaded",
		"backends": strings.Join(backendNames(newCfg.Backends), ", "),
	})
}

// handleStats returns live request statistics.
func handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	statsMu.RLock()
	s := liveStats
	statsMu.RUnlock()

	writeJSON(w, 200, s)
}

func backendNames(configs []BackendConfig) []string {
	names := make([]string, len(configs))
	for i, c := range configs {
		names[i] = c.Name
	}
	return names
}

// saveConfigToDisk writes the current in-memory config to the YAML file.
func saveConfigToDisk() {
	configPath := env("CONFIG_PATH", "config.yaml")

	configMu.RLock()
	data, err := marshalConfig(cfg)
	configMu.RUnlock()

	if err != nil {
		logger.Printf("failed to marshal config: %v", err)
		return
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		logger.Printf("failed to save config: %v", err)
	}
}
