package main

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration structure.
type Config struct {
	Server     ServerConfig                `yaml:"server"`
	Backends   []BackendConfig             `yaml:"backends"`
	Rules      []RoutingRule               `yaml:"rules"`
	Budgets    map[string]BudgetConfig     `yaml:"budgets"`
	RateLimits map[string]RateLimitConfig  `yaml:"rate_limits"`
	Batching   BatchingConfig              `yaml:"batching"`
	Defaults   DefaultsConfig              `yaml:"defaults"`
}

type ServerConfig struct {
	Port                int            `yaml:"port"`
	HealthCheckInterval Duration       `yaml:"health_check_interval"`
	DefaultTimeout      Duration       `yaml:"default_timeout"`
	Branding            BrandingConfig `yaml:"branding"`
}

type BrandingConfig struct {
	Headers         bool   `yaml:"headers"`
	HeaderName      string `yaml:"header_name"`
	ContentInject   bool   `yaml:"content_inject"`
	ContentText     string `yaml:"content_text"`
	ContentSkipJSON bool   `yaml:"content_skip_json"`
}

type BackendConfig struct {
	Name           string   `yaml:"name" json:"name"`
	URL            string   `yaml:"url" json:"url"`
	Type           string   `yaml:"type" json:"type"`
	ModelName      string   `yaml:"model_name" json:"model_name"`
	CostInput1M    float64  `yaml:"cost_input_1m" json:"cost_input_1m"`
	CostOutput1M   float64  `yaml:"cost_output_1m" json:"cost_output_1m"`
	Capabilities   []string `yaml:"capabilities" json:"capabilities"`
	MaxConcurrent  int      `yaml:"max_concurrent" json:"max_concurrent"`
	LoRAAdapters   []string `yaml:"lora_adapters" json:"lora_adapters"`
	APIKey         string   `yaml:"api_key" json:"api_key,omitempty"`
	Dynamic        bool     `yaml:"dynamic" json:"dynamic"`
	HealthEndpoint string   `yaml:"health_endpoint" json:"health_endpoint"`
}

type RoutingRule struct {
	Name     string    `yaml:"name" json:"name"`
	Priority int       `yaml:"priority" json:"priority"`
	Match    RuleMatch `yaml:"match" json:"match"`
	Backends []string  `yaml:"backends" json:"backends"`
	MaxCost  float64   `yaml:"max_cost_1m" json:"max_cost_1m"`
	Required []string  `yaml:"required_capabilities" json:"required_capabilities"`
}

type RuleMatch struct {
	CallType    string `yaml:"call_type" json:"call_type"`
	Service     string `yaml:"service" json:"service"`
	Tier        int    `yaml:"tier" json:"tier"`
	Model       string `yaml:"model" json:"model"`
	LoRA        string `yaml:"lora" json:"lora"`
	Priority    string `yaml:"priority_level" json:"priority_level"`
	ContentType string `yaml:"content_type" json:"content_type"`
}

type BudgetConfig struct {
	DailyLimitUSD   float64 `yaml:"daily_limit_usd" json:"daily_limit_usd"`
	Action          string  `yaml:"action" json:"action"`
	DowngradeTarget string  `yaml:"downgrade_target" json:"downgrade_target"`
}

type BatchingConfig struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	WindowMS       int      `yaml:"window_ms" json:"window_ms"`
	MaxBatchSize   int      `yaml:"max_batch_size" json:"max_batch_size"`
	PriorityBypass []string `yaml:"priority_bypass" json:"priority_bypass"`
}

type DefaultsConfig struct {
	FallbackChain    []string `yaml:"fallback_chain"`
	DefaultTimeoutMS int      `yaml:"default_timeout_ms"`
}

// Duration wraps time.Duration for YAML unmarshalling.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// loadConfig reads and parses the YAML configuration file.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadConfigFromBytes(data)
}

// loadConfigFromBytes parses YAML config from raw bytes.
func loadConfigFromBytes(data []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, err
	}
	applyDefaults(c)
	resolveEnvVars(c)
	sortRules(c)
	return c, nil
}

// marshalConfig serialises the current config to YAML.
func marshalConfig(c *Config) ([]byte, error) {
	return yaml.Marshal(c)
}

func applyDefaults(c *Config) {
	if c.Server.Port == 0 {
		c.Server.Port = 8050
	}
	if c.Server.HealthCheckInterval.Duration == 0 {
		c.Server.HealthCheckInterval.Duration = 30 * time.Second
	}
	if c.Server.DefaultTimeout.Duration == 0 {
		c.Server.DefaultTimeout.Duration = 120 * time.Second
	}
	if c.Server.Branding.HeaderName == "" {
		c.Server.Branding.HeaderName = "Kronaxis Router"
	}
	if c.Server.Branding.ContentText == "" {
		c.Server.Branding.ContentText = "\n\n---\n*Powered by [Kronaxis Router](https://kronaxis.co.uk)*"
	}
	if c.Batching.WindowMS == 0 {
		c.Batching.WindowMS = 50
	}
	if c.Batching.MaxBatchSize == 0 {
		c.Batching.MaxBatchSize = 8
	}
	if c.Defaults.DefaultTimeoutMS == 0 {
		c.Defaults.DefaultTimeoutMS = 120000
	}
	for i := range c.Backends {
		if c.Backends[i].MaxConcurrent == 0 {
			c.Backends[i].MaxConcurrent = 10
		}
		if c.Backends[i].HealthEndpoint == "" {
			switch c.Backends[i].Type {
			case "vllm":
				c.Backends[i].HealthEndpoint = "/v1/models"
			default:
				c.Backends[i].HealthEndpoint = "/health"
			}
		}
	}
}

// resolveEnvVars replaces "env:VAR_NAME" values with the actual environment variable.
func resolveEnvVars(c *Config) {
	for i := range c.Backends {
		c.Backends[i].APIKey = resolveEnv(c.Backends[i].APIKey)
		c.Backends[i].URL = resolveEnv(c.Backends[i].URL)
	}
}

func resolveEnv(s string) string {
	if strings.HasPrefix(s, "env:") {
		return os.Getenv(s[4:])
	}
	return s
}

func sortRules(c *Config) {
	sort.Slice(c.Rules, func(i, j int) bool {
		return c.Rules[i].Priority > c.Rules[j].Priority
	})
}

// Config hot-reload via polling.
var (
	configMu      sync.RWMutex
	skipNextReload bool
)

func watchConfig(ctx context.Context, path string) {
	var lastMod time.Time
	info, err := os.Stat(path)
	if err == nil {
		lastMod = info.ModTime()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if !info.ModTime().After(lastMod) {
				continue
			}
			lastMod = info.ModTime()

			// Skip reload if triggered by our own API write
			configMu.Lock()
			if skipNextReload {
				skipNextReload = false
				configMu.Unlock()
				continue
			}
			configMu.Unlock()

			newCfg, err := loadConfig(path)
			if err != nil {
				logger.Printf("config reload failed: %v", err)
				continue
			}

			configMu.Lock()
			cfg = newCfg
			pool.updateBackends(newCfg.Backends)
			rtr.updateRules(newCfg.Rules, newCfg.Defaults)
			bat.updateConfig(newCfg.Batching)
			costs.updateBudgets(newCfg.Budgets)
			rateLim.updateLimits(newCfg.RateLimits)
			configMu.Unlock()

			logger.Printf("config reloaded: %d backends, %d rules",
				len(newCfg.Backends), len(newCfg.Rules))
		}
	}
}
