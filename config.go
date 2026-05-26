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
	Graphify   GraphifyConfig              `yaml:"graphify"`
}

// GraphifyConfig configures the graphify pre-stage middleware (token-saving
// retrieval-augmented generation in front of all backends).
//
// MinCosineSim is *float64 (not float64) so the YAML loader can distinguish
// "field unset → use default 0.4" from "field explicitly 0.0 → no filter".
// All other numeric fields use the standard 0-means-default convention.
type GraphifyConfig struct {
	Enabled             bool                   `yaml:"enabled"`
	Default             string                 `yaml:"default"`               // off | augment | compress | auto
	IngestRoots         []string               `yaml:"ingest_roots"`
	IngestExcludes      []string               `yaml:"ingest_excludes"`
	IngestConcurrency   int                    `yaml:"ingest_concurrency"`    // parallel embed batches
	WatchEnabled        bool                   `yaml:"watch_enabled"`         // run an fsnotify watcher in-process
	TopK                int                    `yaml:"top_k"`
	MinCosineSim        *float64               `yaml:"min_cosine_sim"`
	BM25Weight          float64                `yaml:"bm25_weight"`
	AugmentBudgetChars  int                    `yaml:"augment_budget_chars"`
	CompressBudgetChars int                    `yaml:"compress_budget_chars"`
	AutoCompressChars   int                    `yaml:"auto_compress_chars"`
	AutoAugmentMaxChars int                    `yaml:"auto_augment_max_chars"`
	ServiceOverrides    map[string]string      `yaml:"service_overrides"`     // X-Kronaxis-Service -> mode
	Embedder            GraphifyEmbedderConfig `yaml:"embedder"`

	// ----- Kronaxis Platform integration (Router <-> Fabric) -----
	// When FabricURL is set, the graphify pre-stage delegates retrieval
	// to Fabric's /v1/rag endpoint instead of running embedded pgvector +
	// the local embedder. If unset, embedded behaviour is unchanged so
	// single-box deployments keep working.
	//
	// On any error talking to Fabric we log and fall back to embedded
	// retrieval; we never fail a chat request because Fabric is sad.
	FabricURL        string            `yaml:"fabric_url"`
	FabricKey        string            `yaml:"fabric_key"`         // Bearer token (or env:VAR_NAME)
	FabricRAGWeights *FabricRAGWeights `yaml:"fabric_rag_weights"` // optional override; default pure-cosine
	FabricTimeoutMS  int               `yaml:"fabric_timeout_ms"`  // default 5000
}

// FabricRAGWeights mirrors the weights block sent on /v1/rag. Pointer
// type on the parent so the YAML loader can distinguish "field unset"
// from "field explicitly set with zeros".
type FabricRAGWeights struct {
	Cosine   float64 `yaml:"cosine" json:"cosine"`
	TSVector float64 `yaml:"tsvector" json:"tsvector"`
	Recency  float64 `yaml:"recency" json:"recency"`
}

// EffectiveFabricWeights returns the ranking weights to send to Fabric
// /v1/rag. When the operator didn't set fabric_rag_weights at all we
// default to pure cosine -- Router asks Fabric for code-chunk relevance,
// and Fabric's memo-search default of cosine 0.5 + tsvector 0.3 +
// recency 0.2 is the wrong blend for that workload.
func (g GraphifyConfig) EffectiveFabricWeights() FabricRAGWeights {
	if g.FabricRAGWeights == nil {
		return FabricRAGWeights{Cosine: 1.0, TSVector: 0.0, Recency: 0.0}
	}
	return *g.FabricRAGWeights
}

// FabricEnabled reports whether Router should delegate to Fabric for
// this graphify call. The middleware should fall back to embedded
// retrieval if this returns false OR if a Fabric call fails.
func (g GraphifyConfig) FabricEnabled() bool {
	return strings.TrimSpace(g.FabricURL) != ""
}

// EffectiveMinCosineSim returns the float to pass to the retriever:
// 0.4 if the operator didn't set the field at all, otherwise their value
// (including 0.0, which means "no filter").
func (g GraphifyConfig) EffectiveMinCosineSim() float64 {
	if g.MinCosineSim == nil {
		return 0.4
	}
	return *g.MinCosineSim
}

type GraphifyEmbedderConfig struct {
	Type      string `yaml:"type"`        // local-st | gemini | openai
	URL       string `yaml:"url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	Dim       int    `yaml:"dim"`
}

func (g GraphifyConfig) WithDefaults() GraphifyConfig {
	if g.TopK == 0 {
		g.TopK = 5
	}
	// MinCosineSim is *float64; nil means "unset, use default", and the
	// EffectiveMinCosineSim() helper resolves it at call sites. We don't
	// initialize a pointer here because that would erase the unset state.
	if g.BM25Weight == 0 {
		g.BM25Weight = 0.3
	}
	if g.AugmentBudgetChars == 0 {
		g.AugmentBudgetChars = 3200
	}
	if g.CompressBudgetChars == 0 {
		g.CompressBudgetChars = 4800
	}
	if g.AutoCompressChars == 0 {
		g.AutoCompressChars = 8000
	}
	if g.AutoAugmentMaxChars == 0 {
		g.AutoAugmentMaxChars = 4000
	}
	if g.Default == "" {
		g.Default = "off"
	}
	if g.Embedder.Type == "" {
		g.Embedder.Type = "local-st"
	}
	if g.Embedder.URL == "" && g.Embedder.Type == "local-st" {
		g.Embedder.URL = "http://localhost:8053"
	}
	if g.IngestConcurrency <= 0 {
		g.IngestConcurrency = 4
	}
	if g.ServiceOverrides == nil {
		g.ServiceOverrides = map[string]string{}
	}
	if g.FabricTimeoutMS <= 0 {
		g.FabricTimeoutMS = 5000
	}
	// Allow env:VAR_NAME for the bearer token.
	g.FabricKey = resolveEnv(g.FabricKey)
	g.FabricURL = strings.TrimRight(strings.TrimSpace(g.FabricURL), "/")
	return g
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
	Name           string             `yaml:"name" json:"name"`
	URL            string             `yaml:"url" json:"url"`
	Type           string             `yaml:"type" json:"type"`
	ModelName      string             `yaml:"model_name" json:"model_name"`
	CostInput1M    float64            `yaml:"cost_input_1m" json:"cost_input_1m"`
	CostOutput1M   float64            `yaml:"cost_output_1m" json:"cost_output_1m"`
	Capabilities   []string           `yaml:"capabilities" json:"capabilities"`
	MaxConcurrent  int                `yaml:"max_concurrent" json:"max_concurrent"`
	LoRAAdapters   []string           `yaml:"lora_adapters" json:"lora_adapters"`
	APIKey         string             `yaml:"api_key" json:"api_key,omitempty"`
	Dynamic        bool               `yaml:"dynamic" json:"dynamic"`
	HealthEndpoint string             `yaml:"health_endpoint" json:"health_endpoint"`
	KVPinning      *KVPinningConfig   `yaml:"kv_pinning,omitempty" json:"kv_pinning,omitempty"`
	// CacheBreakpoints, when true, instructs the proxy to inject
	// provider specific cache markers (currently Anthropic ephemeral)
	// onto the stable prefix of the messages array before forwarding.
	// Stacks with stateful sessions: sessions store the full transcript
	// once on the gateway; cache breakpoints make the provider's own
	// cache hit on the same prefix on every subsequent turn.
	//
	// Off by default. Only enable for backends whose API understands
	// the Anthropic content array + cache_control: ephemeral shape
	// (Anthropic native, or OpenRouter / similar gateways that pass
	// it through).
	CacheBreakpoints bool `yaml:"cache_breakpoints,omitempty" json:"cache_breakpoints,omitempty"`
}

// KVPinningConfig enables prefix-hash routing for a backend. When set,
// the router maintains a per-backend tree of recently-seen prompt
// prefixes and biases routing toward the backend with the deepest
// matching prefix (its vLLM KV cache is presumed warm for that path).
//
// Sane defaults: max_prefix_age_seconds = 600 (10 min), hash_chunk_tokens = 128.
// Set Enabled: true at minimum to opt in.
type KVPinningConfig struct {
	Enabled             bool `yaml:"enabled" json:"enabled"`
	MaxPrefixAgeSeconds int  `yaml:"max_prefix_age_seconds" json:"max_prefix_age_seconds"`
	HashChunkTokens     int  `yaml:"hash_chunk_tokens" json:"hash_chunk_tokens"`
	MaxNodes            int  `yaml:"max_nodes" json:"max_nodes"`
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
