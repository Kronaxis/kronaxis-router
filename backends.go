package main

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// BackendStatus represents the health state of a backend.
type BackendStatus int

const (
	StatusHealthy  BackendStatus = iota
	StatusDegraded               // 1 consecutive failure
	StatusDown                   // 3+ consecutive failures
)

func (s BackendStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusDegraded:
		return "degraded"
	case StatusDown:
		return "down"
	default:
		return "unknown"
	}
}

// Backend is a single LLM endpoint with health and concurrency tracking.
type Backend struct {
	Config      BackendConfig
	Status      BackendStatus
	LastCheck   time.Time
	LastLatency time.Duration
	Failures    int
	ActiveReqs  atomic.Int64
	mu          sync.RWMutex
}

// HasCapability checks whether the backend declares a given capability.
func (b *Backend) HasCapability(cap string) bool {
	for _, c := range b.Config.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// HasAdapter checks whether the backend has a given LoRA adapter loaded.
func (b *Backend) HasAdapter(adapter string) bool {
	for _, a := range b.Config.LoRAAdapters {
		if a == adapter {
			return true
		}
	}
	return false
}

// IsAvailable returns true if the backend is healthy (or degraded) and not at capacity.
func (b *Backend) IsAvailable() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.Status == StatusDown {
		return false
	}
	if b.Config.MaxConcurrent > 0 && int(b.ActiveReqs.Load()) >= b.Config.MaxConcurrent {
		return false
	}
	return true
}

// BackendPool manages all backends and runs health checks.
type BackendPool struct {
	backends map[string]*Backend
	mu       sync.RWMutex
}

func newBackendPool(configs []BackendConfig) *BackendPool {
	bp := &BackendPool{
		backends: make(map[string]*Backend),
	}
	for _, c := range configs {
		bp.backends[c.Name] = &Backend{
			Config: c,
			Status: StatusHealthy, // Assume healthy until first check
		}
	}
	return bp
}

// Get returns a backend by name, or nil.
func (bp *BackendPool) Get(name string) *Backend {
	bp.mu.RLock()
	defer bp.mu.RUnlock()
	return bp.backends[name]
}

// GetHealthy filters a list of backend names to those that are healthy,
// available, and have all required capabilities.
func (bp *BackendPool) GetHealthy(names []string, requiredCaps []string) []*Backend {
	bp.mu.RLock()
	defer bp.mu.RUnlock()

	var result []*Backend
	for _, name := range names {
		b, ok := bp.backends[name]
		if !ok {
			continue
		}
		if !b.IsAvailable() {
			continue
		}
		// Check required capabilities
		capsOK := true
		for _, cap := range requiredCaps {
			if !b.HasCapability(cap) {
				capsOK = false
				break
			}
		}
		if !capsOK {
			continue
		}
		result = append(result, b)
	}
	return result
}

// GetWithAdapter filters to backends that have a specific LoRA adapter.
func (bp *BackendPool) GetWithAdapter(names []string, adapter string) []*Backend {
	bp.mu.RLock()
	defer bp.mu.RUnlock()

	var result []*Backend
	for _, name := range names {
		b, ok := bp.backends[name]
		if !ok || !b.IsAvailable() {
			continue
		}
		if b.HasAdapter(adapter) {
			result = append(result, b)
		}
	}
	return result
}

// Register adds or updates a dynamic backend.
func (bp *BackendPool) Register(cfg BackendConfig) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if existing, ok := bp.backends[cfg.Name]; ok {
		existing.mu.Lock()
		existing.Config = cfg
		existing.mu.Unlock()
	} else {
		bp.backends[cfg.Name] = &Backend{
			Config: cfg,
			Status: StatusHealthy,
		}
	}
}

// Deregister removes a backend by name.
func (bp *BackendPool) Deregister(name string) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	delete(bp.backends, name)
}

func (bp *BackendPool) updateBackends(configs []BackendConfig) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// Track which backends are in the new config
	seen := make(map[string]bool)
	for _, c := range configs {
		seen[c.Name] = true
		if existing, ok := bp.backends[c.Name]; ok {
			existing.mu.Lock()
			existing.Config = c
			existing.mu.Unlock()
		} else {
			bp.backends[c.Name] = &Backend{
				Config: c,
				Status: StatusHealthy,
			}
		}
	}
	// Remove backends not in new config (unless dynamic)
	for name, b := range bp.backends {
		if !seen[name] && !b.Config.Dynamic {
			delete(bp.backends, name)
		}
	}
}

type backendStatusInfo struct {
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Type         string  `json:"type"`
	URL          string  `json:"url"`
	ActiveReqs   int64   `json:"active_requests"`
	LastCheckMS  int64   `json:"last_check_ms"`
	LatencyMS    int64   `json:"latency_ms"`
	CostInput1M  float64 `json:"cost_input_1m"`
	CostOutput1M float64 `json:"cost_output_1m"`
}

func (bp *BackendPool) allStatuses() []backendStatusInfo {
	bp.mu.RLock()
	defer bp.mu.RUnlock()

	result := make([]backendStatusInfo, 0, len(bp.backends))
	for _, b := range bp.backends {
		b.mu.RLock()
		info := backendStatusInfo{
			Name:         b.Config.Name,
			Status:       b.Status.String(),
			Type:         b.Config.Type,
			URL:          b.Config.URL,
			ActiveReqs:   b.ActiveReqs.Load(),
			LastCheckMS:  time.Since(b.LastCheck).Milliseconds(),
			LatencyMS:    b.LastLatency.Milliseconds(),
			CostInput1M:  b.Config.CostInput1M,
			CostOutput1M: b.Config.CostOutput1M,
		}
		b.mu.RUnlock()
		result = append(result, info)
	}
	return result
}

// startHealthChecks runs periodic health probes against all backends.
func (bp *BackendPool) startHealthChecks(ctx context.Context, interval time.Duration) {
	// Run immediately on startup
	bp.checkAll()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bp.checkAll()
			}
		}
	}()
}

func (bp *BackendPool) checkAll() {
	bp.mu.RLock()
	backends := make([]*Backend, 0, len(bp.backends))
	for _, b := range bp.backends {
		backends = append(backends, b)
	}
	bp.mu.RUnlock()

	for _, b := range backends {
		go bp.checkOne(b)
	}
}

func (bp *BackendPool) checkOne(b *Backend) {
	// Skip health checks for cloud APIs (Gemini, OpenAI) -- they are always "available"
	if b.Config.Type == "gemini" || b.Config.Type == "openai" {
		b.mu.Lock()
		b.Status = StatusHealthy
		b.LastCheck = time.Now()
		b.LastLatency = 0
		b.Failures = 0
		b.mu.Unlock()
		return
	}

	if b.Config.HealthEndpoint == "" {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := b.Config.URL + b.Config.HealthEndpoint

	start := time.Now()
	resp, err := client.Get(url)
	latency := time.Since(start)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.LastCheck = time.Now()
	b.LastLatency = latency

	if err != nil || (resp != nil && resp.StatusCode >= 500) {
		b.Failures++
		if b.Failures >= 3 {
			if b.Status != StatusDown {
				logger.Printf("backend %s marked DOWN (%d failures)", b.Config.Name, b.Failures)
			}
			b.Status = StatusDown
		} else {
			b.Status = StatusDegraded
		}
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	if resp != nil {
		resp.Body.Close()
	}

	if b.Status != StatusHealthy {
		logger.Printf("backend %s recovered (was %s)", b.Config.Name, b.Status)
	}
	b.Status = StatusHealthy
	b.Failures = 0
}
