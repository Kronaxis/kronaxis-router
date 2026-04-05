package main

import (
	"net/http"
	"sync"
	"time"
)

// RateLimitConfig defines per-service request rate limits.
type RateLimitConfig struct {
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`
	BurstSize         int     `yaml:"burst_size" json:"burst_size"`
}

// RateLimiter implements a token bucket rate limiter per service.
type RateLimiter struct {
	limits  map[string]RateLimitConfig
	buckets map[string]*tokenBucket
	mu      sync.RWMutex
}

type tokenBucket struct {
	tokens    float64
	maxTokens float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func newRateLimiter(limits map[string]RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		limits:  limits,
		buckets: make(map[string]*tokenBucket),
	}
	return rl
}

func (rl *RateLimiter) updateLimits(limits map[string]RateLimitConfig) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limits = limits
	// Reset buckets so new limits take effect
	rl.buckets = make(map[string]*tokenBucket)
}

// Allow checks whether a request from the given service is allowed.
// Returns true if allowed, false if rate limited.
func (rl *RateLimiter) Allow(service string) bool {
	if service == "" {
		return true
	}

	rl.mu.RLock()
	limit, ok := rl.limits[service]
	if !ok {
		limit, ok = rl.limits["default"]
	}
	rl.mu.RUnlock()

	if !ok || limit.RequestsPerSecond <= 0 {
		return true // No limit configured
	}

	bucket := rl.getBucket(service, limit)
	return bucket.take()
}

func (rl *RateLimiter) getBucket(service string, limit RateLimitConfig) *tokenBucket {
	rl.mu.RLock()
	b, ok := rl.buckets[service]
	rl.mu.RUnlock()
	if ok {
		return b
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if b, ok := rl.buckets[service]; ok {
		return b
	}

	burst := limit.BurstSize
	if burst <= 0 {
		burst = int(limit.RequestsPerSecond) * 2
		if burst < 5 {
			burst = 5
		}
	}

	b = &tokenBucket{
		tokens:     float64(burst),
		maxTokens:  float64(burst),
		refillRate: limit.RequestsPerSecond,
		lastRefill: time.Now(),
	}
	rl.buckets[service] = b
	return b
}

func (tb *tokenBucket) take() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now

	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

// rateLimitMiddleware enforces per-service rate limits.
func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only rate limit the proxy endpoint
		if r.URL.Path != "/v1/chat/completions" {
			next.ServeHTTP(w, r)
			return
		}

		service := r.Header.Get("X-Kronaxis-Service")
		if !rateLim.Allow(service) {
			w.Header().Set("Retry-After", "1")
			writeErrorJSON(w, 429, "rate limit exceeded for service: "+service)
			return
		}

		next.ServeHTTP(w, r)
	})
}
