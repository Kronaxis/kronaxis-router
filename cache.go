package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// ResponseCache stores LLM responses keyed by prompt hash.
// Identical requests (same model, messages, temperature) return cached
// responses without calling the backend, saving both time and cost.
type ResponseCache struct {
	entries map[string]*cacheEntry
	maxSize int
	ttl     time.Duration
	enabled bool
	hits    int64
	misses  int64
	mu      sync.RWMutex
}

type cacheEntry struct {
	Response   []byte
	StatusCode int
	Headers    map[string]string
	CreatedAt  time.Time
	HitCount   int64
}

func newResponseCache(maxSize int, ttlSeconds int) *ResponseCache {
	rc := &ResponseCache{
		entries: make(map[string]*cacheEntry),
		maxSize: maxSize,
		ttl:     time.Duration(ttlSeconds) * time.Second,
		enabled: maxSize > 0 && ttlSeconds > 0,
	}
	if rc.enabled {
		go rc.evictionLoop()
	}
	return rc
}

// cacheKey generates a deterministic hash from the request.
// Only caches when temperature is 0 (deterministic output).
func cacheKey(req *ChatRequest) (string, bool) {
	// Only cache explicitly deterministic requests (temperature must be set to 0)
	if req.Temperature == nil || *req.Temperature > 0 {
		return "", false
	}

	// Don't cache streaming requests
	if req.Stream {
		return "", false
	}

	// Build a canonical key from all parameters that affect output
	key := struct {
		Model     string        `json:"m"`
		Messages  []ChatMessage `json:"msgs"`
		MaxTokens int           `json:"mt"`
		TopP      *float64      `json:"tp,omitempty"`
		N         int           `json:"n,omitempty"`
	}{
		Model:     req.Model,
		Messages:  req.Messages,
		MaxTokens: req.MaxTokens,
		TopP:      req.TopP,
		N:         req.N,
	}

	data, err := json.Marshal(key)
	if err != nil {
		return "", false
	}

	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:16]), true // 128-bit hash, 32 hex chars
}

// Get returns a cached response if available.
func (rc *ResponseCache) Get(key string) ([]byte, int, map[string]string, bool) {
	if !rc.enabled {
		return nil, 0, nil, false
	}

	rc.mu.RLock()
	entry, ok := rc.entries[key]
	rc.mu.RUnlock()

	if !ok {
		rc.mu.Lock()
		rc.misses++
		rc.mu.Unlock()
		return nil, 0, nil, false
	}

	// Check TTL
	if time.Since(entry.CreatedAt) > rc.ttl {
		rc.mu.Lock()
		delete(rc.entries, key)
		rc.misses++
		rc.mu.Unlock()
		return nil, 0, nil, false
	}

	rc.mu.Lock()
	entry.HitCount++
	rc.hits++
	rc.mu.Unlock()

	return entry.Response, entry.StatusCode, entry.Headers, true
}

// Set stores a response in the cache.
func (rc *ResponseCache) Set(key string, response []byte, statusCode int, headers map[string]string) {
	if !rc.enabled {
		return
	}

	// Only cache successful responses
	if statusCode >= 400 {
		return
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Evict oldest if at capacity
	if len(rc.entries) >= rc.maxSize {
		var oldestKey string
		var oldestTime time.Time
		for k, v := range rc.entries {
			if oldestKey == "" || v.CreatedAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.CreatedAt
			}
		}
		if oldestKey != "" {
			delete(rc.entries, oldestKey)
		}
	}

	rc.entries[key] = &cacheEntry{
		Response:   response,
		StatusCode: statusCode,
		Headers:    headers,
		CreatedAt:  time.Now(),
	}
}

// Stats returns cache hit/miss statistics.
func (rc *ResponseCache) Stats() map[string]interface{} {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	hitRate := float64(0)
	total := rc.hits + rc.misses
	if total > 0 {
		hitRate = float64(rc.hits) / float64(total) * 100
	}

	return map[string]interface{}{
		"enabled":  rc.enabled,
		"size":     len(rc.entries),
		"max_size": rc.maxSize,
		"ttl_s":    int(rc.ttl.Seconds()),
		"hits":     rc.hits,
		"misses":   rc.misses,
		"hit_rate": hitRate,
	}
}

func (rc *ResponseCache) evictionLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		rc.mu.Lock()
		now := time.Now()
		for k, v := range rc.entries {
			if now.Sub(v.CreatedAt) > rc.ttl {
				delete(rc.entries, k)
			}
		}
		rc.mu.Unlock()
	}
}
