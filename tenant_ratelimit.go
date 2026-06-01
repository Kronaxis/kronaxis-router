package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Per-tenant token-bucket rate limiting for the chat-completions proxy.
//
// Why this exists (BoS Completion Spec §3.4 row 5.4):
//
//   The existing per-service RateLimiter (ratelimit.go) treats every caller
//   of a service identically. In production one BoS tenant can burst the
//   shared Imprint vLLM and starve every other tenant and every other
//   internal agent that needs an LLM. That is a multi-tenant isolation
//   failure, not a backend-capacity failure -- the only correct fix is to
//   meter requests per tenant before they reach the backend pool.
//
// What this gives you:
//
//   * Each tenant gets its own refill-rate-limited token bucket.
//   * A runaway tenant hits 429 with Retry-After. Other tenants are
//     unaffected because they have their own buckets.
//   * Internal services that must never be rate-limited (typically
//     controld, billing-agentd, updaterd) can be put on the bypass list.
//   * Per-tenant overrides let a paid sovereign tenant get a higher cap
//     than the default free tier.
//
// What this does NOT do:
//
//   * It does NOT replace the per-service backend pool's MaxConcurrent
//     caps. Backend protection is still the backend's job.
//   * It does NOT replace the per-service RateLimiter. Both run; the
//     per-tenant one runs first.

// ErrRateLimited is returned by TokenBucket.Acquire when the request cannot
// be satisfied without waiting longer than the caller's context allows.
var ErrRateLimited = errors.New("tenant rate limit exceeded")

// TokenBucket is a classic token bucket: capacity tokens, refilled at
// RefillPerSec tokens/second. Acquire(n) blocks until n tokens are
// available, the context cancels, or the context's deadline cannot be
// satisfied by the bucket's refill rate.
//
// Safe for concurrent use by many goroutines.
type TokenBucket struct {
	mu sync.Mutex

	tokens       float64
	maxTokens    float64
	refillPerSec float64
	lastRefill   time.Time
}

// NewTokenBucket builds a bucket with given capacity and refill rate.
//
// refillPerSec must be > 0 and capacity must be > 0. The bucket starts
// full. Refill rate is honoured at the second granularity but updated
// fractionally on every call -- a 60-per-minute bucket refills at 1.0
// tokens/sec continuously, not in batches of 60 once a minute.
func NewTokenBucket(capacity int, refillPerSec float64) *TokenBucket {
	if capacity <= 0 {
		capacity = 1
	}
	if refillPerSec <= 0 {
		refillPerSec = 1
	}
	return &TokenBucket{
		tokens:       float64(capacity),
		maxTokens:    float64(capacity),
		refillPerSec: refillPerSec,
		lastRefill:   time.Now(),
	}
}

// refillLocked applies elapsed-time refill. Caller MUST hold tb.mu.
func (tb *TokenBucket) refillLocked(now time.Time) {
	elapsed := now.Sub(tb.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	tb.tokens += elapsed * tb.refillPerSec
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now
}

// TryAcquire grabs n tokens without blocking. Returns true if successful.
func (tb *TokenBucket) TryAcquire(n int) bool {
	if n <= 0 {
		return true
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refillLocked(time.Now())
	if tb.tokens < float64(n) {
		return false
	}
	tb.tokens -= float64(n)
	return true
}

// Acquire blocks until n tokens are available, the context cancels, or
// the wait would exceed the context deadline.
//
// Returns nil on success, ctx.Err() on cancellation/deadline, ErrRateLimited
// if the context has a deadline that won't allow enough refill time.
func (tb *TokenBucket) Acquire(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tb.mu.Lock()
	tb.refillLocked(time.Now())
	if tb.tokens >= float64(n) {
		tb.tokens -= float64(n)
		tb.mu.Unlock()
		return nil
	}

	// Compute how long until n tokens are available.
	deficit := float64(n) - tb.tokens
	wait := time.Duration(deficit / tb.refillPerSec * float64(time.Second))
	tb.mu.Unlock()

	// Refuse upfront if the caller's deadline can't accommodate the wait.
	if deadline, ok := ctx.Deadline(); ok {
		if time.Until(deadline) < wait {
			return ErrRateLimited
		}
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		// Try once more; refill may have happened. Worst case, return
		// ErrRateLimited so callers don't loop forever under heavy
		// contention.
		tb.mu.Lock()
		defer tb.mu.Unlock()
		tb.refillLocked(time.Now())
		if tb.tokens < float64(n) {
			return ErrRateLimited
		}
		tb.tokens -= float64(n)
		return nil
	}
}

// Tokens returns the current available token count (after refilling for
// the call time). Used for /metrics gauge.
func (tb *TokenBucket) Tokens() float64 {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refillLocked(time.Now())
	return tb.tokens
}

// TenantRateLimitConfig governs per-tenant token bucket behaviour.
type TenantRateLimitConfig struct {
	// DefaultRPM is the per-tenant requests-per-minute cap when no
	// override is set. Default 60.
	DefaultRPM float64 `yaml:"default_rpm" json:"default_rpm"`

	// DefaultBurst is the per-tenant burst capacity (max tokens).
	// Default 10.
	DefaultBurst int `yaml:"default_burst" json:"default_burst"`

	// UnattributedRPM applies to requests with no resolvable tenant.
	// Lower than DefaultRPM by design -- unknown callers are not
	// trusted. Default 30.
	UnattributedRPM float64 `yaml:"unattributed_rpm" json:"unattributed_rpm"`

	// UnattributedBurst is the burst capacity for unattributed callers.
	// Default 5.
	UnattributedBurst int `yaml:"unattributed_burst" json:"unattributed_burst"`

	// BypassTenants is the explicit allow-list of tenant IDs that
	// never get rate-limited (typically internal services like
	// controld, billing-agentd, updaterd, system).
	BypassTenants []string `yaml:"bypass_tenants" json:"bypass_tenants"`

	// Overrides lets a specific tenant get a higher (or lower) cap.
	// Map key is the tenant ID. RPM and Burst default to the
	// global defaults if zero.
	Overrides map[string]TenantOverride `yaml:"overrides" json:"overrides"`

	// Disabled turns off all per-tenant rate limiting (for emergencies).
	Disabled bool `yaml:"disabled" json:"disabled"`
}

// TenantOverride is a per-tenant rate cap.
type TenantOverride struct {
	RPM   float64 `yaml:"rpm" json:"rpm"`
	Burst int     `yaml:"burst" json:"burst"`
}

// TenantRateLimiter is the per-tenant token-bucket registry.
type TenantRateLimiter struct {
	mu      sync.RWMutex
	cfg     TenantRateLimitConfig
	bypass  map[string]struct{}
	buckets map[string]*TokenBucket

	// Metrics counters. Map key = tenant ID, then by decision.
	metricMu       sync.Mutex
	allowedTotal   map[string]*atomic.Int64
	rateLimitTotal map[string]*atomic.Int64
}

// NewTenantRateLimiter builds a registry. Use applyDefaults() if the cfg
// might be partially zeroed (e.g. straight from YAML).
func NewTenantRateLimiter(cfg TenantRateLimitConfig) *TenantRateLimiter {
	cfg = cfg.WithDefaults()
	bypass := make(map[string]struct{}, len(cfg.BypassTenants))
	for _, t := range cfg.BypassTenants {
		bypass[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	return &TenantRateLimiter{
		cfg:            cfg,
		bypass:         bypass,
		buckets:        make(map[string]*TokenBucket),
		allowedTotal:   make(map[string]*atomic.Int64),
		rateLimitTotal: make(map[string]*atomic.Int64),
	}
}

// WithDefaults returns a copy with zero fields filled with sensible values.
func (c TenantRateLimitConfig) WithDefaults() TenantRateLimitConfig {
	if c.DefaultRPM <= 0 {
		c.DefaultRPM = 60
	}
	if c.DefaultBurst <= 0 {
		c.DefaultBurst = 10
	}
	if c.UnattributedRPM <= 0 {
		c.UnattributedRPM = 30
	}
	if c.UnattributedBurst <= 0 {
		c.UnattributedBurst = 5
	}
	if c.Overrides == nil {
		c.Overrides = map[string]TenantOverride{}
	}
	return c
}

// UpdateConfig swaps the configuration. Existing buckets are reset so the
// new limits take effect immediately.
func (rl *TenantRateLimiter) UpdateConfig(cfg TenantRateLimitConfig) {
	cfg = cfg.WithDefaults()
	bypass := make(map[string]struct{}, len(cfg.BypassTenants))
	for _, t := range cfg.BypassTenants {
		bypass[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.cfg = cfg
	rl.bypass = bypass
	rl.buckets = make(map[string]*TokenBucket)
}

// IsBypass returns true if the tenant should not be rate-limited.
func (rl *TenantRateLimiter) IsBypass(tenantID string) bool {
	if tenantID == "" {
		return false
	}
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	_, ok := rl.bypass[strings.ToLower(strings.TrimSpace(tenantID))]
	return ok
}

// Allow returns (allowed, retryAfter). retryAfter is set when allowed=false.
// Non-blocking. Use Acquire if you want to wait for a token.
func (rl *TenantRateLimiter) Allow(tenantID string) (bool, time.Duration) {
	rl.mu.RLock()
	disabled := rl.cfg.Disabled
	rl.mu.RUnlock()
	if disabled {
		rl.recordDecision(tenantID, true)
		return true, 0
	}
	if rl.IsBypass(tenantID) {
		rl.recordDecision(tenantID, true)
		return true, 0
	}
	key := rl.normaliseKey(tenantID)
	bucket := rl.getOrCreateBucket(key)
	if bucket.TryAcquire(1) {
		rl.recordDecision(key, true)
		return true, 0
	}
	rl.recordDecision(key, false)
	// Approximate Retry-After: time to refill one token from empty.
	bucket.mu.Lock()
	refillRate := bucket.refillPerSec
	bucket.mu.Unlock()
	retry := time.Second
	if refillRate > 0 {
		retry = time.Duration(1.0 / refillRate * float64(time.Second))
		if retry < time.Second {
			retry = time.Second
		}
	}
	return false, retry
}

// Acquire is the blocking variant. Honours context deadline.
func (rl *TenantRateLimiter) Acquire(ctx context.Context, tenantID string) error {
	rl.mu.RLock()
	disabled := rl.cfg.Disabled
	rl.mu.RUnlock()
	if disabled {
		rl.recordDecision(tenantID, true)
		return nil
	}
	if rl.IsBypass(tenantID) {
		rl.recordDecision(tenantID, true)
		return nil
	}
	key := rl.normaliseKey(tenantID)
	bucket := rl.getOrCreateBucket(key)
	if err := bucket.Acquire(ctx, 1); err != nil {
		rl.recordDecision(key, false)
		return err
	}
	rl.recordDecision(key, true)
	return nil
}

// normaliseKey returns the bucket key for the given tenant ID. Empty
// strings become "unattributed".
func (rl *TenantRateLimiter) normaliseKey(tenantID string) string {
	k := strings.ToLower(strings.TrimSpace(tenantID))
	if k == "" {
		return "unattributed"
	}
	return k
}

// getOrCreateBucket returns the bucket for a key, creating it on first use.
func (rl *TenantRateLimiter) getOrCreateBucket(key string) *TokenBucket {
	rl.mu.RLock()
	b, ok := rl.buckets[key]
	rl.mu.RUnlock()
	if ok {
		return b
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if b, ok := rl.buckets[key]; ok {
		return b
	}
	// Pick rate + burst based on overrides / unattributed / defaults.
	rpm, burst := rl.cfg.DefaultRPM, rl.cfg.DefaultBurst
	if key == "unattributed" {
		rpm, burst = rl.cfg.UnattributedRPM, rl.cfg.UnattributedBurst
	} else if ov, ok := rl.cfg.Overrides[key]; ok {
		if ov.RPM > 0 {
			rpm = ov.RPM
		}
		if ov.Burst > 0 {
			burst = ov.Burst
		}
	}
	b = NewTokenBucket(burst, rpm/60.0)
	rl.buckets[key] = b
	return b
}

// recordDecision bumps the per-tenant Prometheus counter.
func (rl *TenantRateLimiter) recordDecision(tenantID string, allowed bool) {
	key := rl.normaliseKey(tenantID)
	rl.metricMu.Lock()
	defer rl.metricMu.Unlock()
	if allowed {
		c, ok := rl.allowedTotal[key]
		if !ok {
			c = &atomic.Int64{}
			rl.allowedTotal[key] = c
		}
		c.Add(1)
		return
	}
	c, ok := rl.rateLimitTotal[key]
	if !ok {
		c = &atomic.Int64{}
		rl.rateLimitTotal[key] = c
	}
	c.Add(1)
}

// snapshot returns the current metric values for /metrics rendering.
func (rl *TenantRateLimiter) snapshot() (allowed map[string]int64, limited map[string]int64, tokens map[string]float64) {
	rl.metricMu.Lock()
	allowed = make(map[string]int64, len(rl.allowedTotal))
	for k, v := range rl.allowedTotal {
		allowed[k] = v.Load()
	}
	limited = make(map[string]int64, len(rl.rateLimitTotal))
	for k, v := range rl.rateLimitTotal {
		limited[k] = v.Load()
	}
	rl.metricMu.Unlock()

	rl.mu.RLock()
	bucketKeys := make([]string, 0, len(rl.buckets))
	for k := range rl.buckets {
		bucketKeys = append(bucketKeys, k)
	}
	rl.mu.RUnlock()
	tokens = make(map[string]float64, len(bucketKeys))
	for _, k := range bucketKeys {
		rl.mu.RLock()
		b := rl.buckets[k]
		rl.mu.RUnlock()
		if b != nil {
			tokens[k] = b.Tokens()
		}
	}
	return
}

// extractTenant resolves the tenant ID for a request.
//
// Resolution order:
//  1. X-Kronaxis-Tenant-ID header (canonical; every BoS agent should set this)
//  2. X-Kronaxis-Service header as a coarse fallback (legacy callers)
//  3. "" -- caller is unattributed and routed to the lower-limit bucket
func extractTenant(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-Kronaxis-Tenant-ID")); t != "" {
		return t
	}
	// Legacy fallback: many existing internal callers set Service but not
	// Tenant-ID. Treat the service name as the tenant for now so existing
	// usage doesn't all collapse onto "unattributed" and starve.
	if s := strings.TrimSpace(r.Header.Get("X-Kronaxis-Service")); s != "" {
		return s
	}
	return ""
}

// tenantRateLimitMiddleware enforces per-tenant rate limits on the proxy
// endpoint. Returns 429 with Retry-After header on rejection.
//
// Wires AFTER auth, BEFORE the per-service ratelimit and the proxy
// handler itself. (See main.go middleware chain.)
func tenantRateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only meter the proxy endpoint. Health checks, /api/*, etc.
		// are deliberately not metered.
		if !shouldMeter(r) {
			next.ServeHTTP(w, r)
			return
		}
		if tenantRateLim == nil {
			next.ServeHTTP(w, r)
			return
		}
		tenant := extractTenant(r)
		allowed, retryAfter := tenantRateLim.Allow(tenant)
		if !allowed {
			retrySec := int(retryAfter.Seconds())
			if retrySec < 1 {
				retrySec = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySec))
			writeErrorJSON(w, http.StatusTooManyRequests,
				fmt.Sprintf("per-tenant rate limit exceeded for tenant %q; retry in %ds", tenant, retrySec))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// shouldMeter returns true for endpoints that consume backend capacity.
// We meter chat completions (the LLM hot path) and video generation
// (separate-pool but still a finite GPU resource).
func shouldMeter(r *http.Request) bool {
	path := r.URL.Path
	switch path {
	case "/v1/chat/completions":
		return true
	case "/v1/video/generate":
		return true
	}
	return false
}
