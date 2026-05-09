package main

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port                 int    `yaml:"port"`
	ClaudeBinary         string `yaml:"claude_binary"`
	WorkspaceRoot        string `yaml:"workspace_root"`
	TimeoutSeconds       int    `yaml:"timeout_seconds"`
	MaxConcurrent        int    `yaml:"max_concurrent"`
	BaseRepo             string `yaml:"base_repo"`
	RetainWorkspaces     bool   `yaml:"retain_workspaces"`
	KeepaliveSeconds     int    `yaml:"keepalive_seconds"`
	WorkspaceMaxAgeHours int    `yaml:"workspace_max_age_hours"`
	SweepIntervalMinutes int    `yaml:"sweep_interval_minutes"`
	AuditFile            string `yaml:"audit_file"`
	WarmPoolSize         int    `yaml:"warm_pool_size"`
	GeminiBinary         string `yaml:"gemini_binary"`
	AnthropicBaseURL     string `yaml:"anthropic_base_url"`
	AnthropicAPIKeyEnv   string `yaml:"anthropic_api_key_env"`
	AuthPoolFile         string `yaml:"auth_pool_file"`
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{
		Port:                 8055,
		ClaudeBinary:         "claude",
		WorkspaceRoot:        "/tmp/kx-agent",
		TimeoutSeconds:       600,
		MaxConcurrent:        4,
		KeepaliveSeconds:     15,
		WorkspaceMaxAgeHours: 24,
		SweepIntervalMinutes: 30,
		WarmPoolSize:         0,
		GeminiBinary:         "gemini",
		AnthropicBaseURL:     "https://api.anthropic.com",
		AnthropicAPIKeyEnv:   "ANTHROPIC_API_KEY",
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if v := os.Getenv("AGENT_GATEWAY_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("AGENT_GATEWAY_CLAUDE_BIN"); v != "" {
		cfg.ClaudeBinary = v
	}
	if v := os.Getenv("AGENT_GATEWAY_WORKSPACE_ROOT"); v != "" {
		cfg.WorkspaceRoot = v
	}
	if v := os.Getenv("AGENT_GATEWAY_AUDIT_FILE"); v != "" {
		cfg.AuditFile = v
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 600
	}
	if cfg.KeepaliveSeconds < 0 {
		cfg.KeepaliveSeconds = 0
	}
	if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	return cfg, nil
}
