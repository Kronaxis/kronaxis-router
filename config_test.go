package main

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig("config.yaml")
	if err != nil {
		t.Fatalf("failed to load config.yaml: %v", err)
	}

	if cfg.Server.Port != 8050 {
		t.Errorf("expected port 8050, got %d", cfg.Server.Port)
	}
	if len(cfg.Backends) == 0 {
		t.Error("expected at least one backend")
	}
	if len(cfg.Rules) == 0 {
		t.Error("expected at least one rule")
	}
}

func TestApplyDefaults(t *testing.T) {
	c := &Config{}
	applyDefaults(c)

	if c.Server.Port != 8050 {
		t.Errorf("default port should be 8050, got %d", c.Server.Port)
	}
	if c.Batching.WindowMS != 50 {
		t.Errorf("default window should be 50, got %d", c.Batching.WindowMS)
	}
	if c.Batching.MaxBatchSize != 8 {
		t.Errorf("default batch size should be 8, got %d", c.Batching.MaxBatchSize)
	}
	if c.Server.Branding.HeaderName != "Kronaxis Router" {
		t.Errorf("default branding should be Kronaxis Router, got %s", c.Server.Branding.HeaderName)
	}
}

func TestResolveEnv(t *testing.T) {
	os.Setenv("TEST_RESOLVE_VAR", "hello")
	defer os.Unsetenv("TEST_RESOLVE_VAR")

	if resolveEnv("env:TEST_RESOLVE_VAR") != "hello" {
		t.Error("should resolve env: prefix")
	}
	if resolveEnv("literal") != "literal" {
		t.Error("should pass through non-env values")
	}
	if resolveEnv("env:NONEXISTENT_VAR_12345") != "" {
		t.Error("missing env var should resolve to empty string")
	}
}

func TestSortRules(t *testing.T) {
	c := &Config{
		Rules: []RoutingRule{
			{Name: "low", Priority: 50},
			{Name: "high", Priority: 200},
			{Name: "mid", Priority: 100},
		},
	}
	sortRules(c)

	if c.Rules[0].Name != "high" {
		t.Errorf("first rule should be high priority, got %s", c.Rules[0].Name)
	}
	if c.Rules[2].Name != "low" {
		t.Errorf("last rule should be low priority, got %s", c.Rules[2].Name)
	}
}

func TestLoadConfigFromBytes(t *testing.T) {
	yaml := []byte(`
server:
  port: 9999
backends:
  - name: test
    url: "http://localhost:8000"
    type: vllm
rules:
  - name: rule1
    priority: 100
    backends: [test]
`)
	cfg, err := loadConfigFromBytes(yaml)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Server.Port)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(cfg.Backends))
	}
	if cfg.Backends[0].HealthEndpoint != "/v1/models" {
		t.Errorf("vllm backend should default to /v1/models, got %s", cfg.Backends[0].HealthEndpoint)
	}
}
