package main

import (
	"testing"
)

func TestResponseCache_HitMiss(t *testing.T) {
	cache := newResponseCache(100, 3600)

	cache.Set("key1", []byte(`{"response":"hello"}`), 200, map[string]string{"Content-Type": "application/json"})

	body, status, _, ok := cache.Get("key1")
	if !ok {
		t.Fatal("should have cache hit")
	}
	if status != 200 {
		t.Errorf("expected status 200, got %d", status)
	}
	if string(body) != `{"response":"hello"}` {
		t.Errorf("unexpected body: %s", body)
	}

	_, _, _, ok = cache.Get("nonexistent")
	if ok {
		t.Error("should be cache miss")
	}
}

func TestResponseCache_DoesNotCacheErrors(t *testing.T) {
	cache := newResponseCache(100, 3600)

	cache.Set("err", []byte("error"), 500, nil)
	_, _, _, ok := cache.Get("err")
	if ok {
		t.Error("should not cache 500 responses")
	}
}

func TestResponseCache_Disabled(t *testing.T) {
	cache := newResponseCache(0, 0) // disabled

	cache.Set("key", []byte("data"), 200, nil)
	_, _, _, ok := cache.Get("key")
	if ok {
		t.Error("disabled cache should never hit")
	}
}

func TestResponseCache_MaxSize(t *testing.T) {
	cache := newResponseCache(2, 3600) // max 2 entries

	cache.Set("a", []byte("1"), 200, nil)
	cache.Set("b", []byte("2"), 200, nil)
	cache.Set("c", []byte("3"), 200, nil) // should evict oldest

	if _, _, _, ok := cache.Get("c"); !ok {
		t.Error("c should be in cache")
	}
	if _, _, _, ok := cache.Get("b"); !ok {
		t.Error("b should be in cache")
	}
	// a was evicted
	if _, _, _, ok := cache.Get("a"); ok {
		t.Error("a should have been evicted")
	}
}

func TestCacheKey_DeterministicOnly(t *testing.T) {
	temp0 := float64(0)
	temp07 := float64(0.7)

	// Temperature 0 should be cacheable
	req := &ChatRequest{Model: "test", Temperature: &temp0, Messages: []ChatMessage{{Role: "user", Content: "hello"}}}
	_, cacheable := cacheKey(req)
	if !cacheable {
		t.Error("temp=0 should be cacheable")
	}

	// Temperature 0.7 should NOT be cacheable
	req.Temperature = &temp07
	_, cacheable = cacheKey(req)
	if cacheable {
		t.Error("temp=0.7 should not be cacheable")
	}

	// Streaming should NOT be cacheable
	req.Temperature = &temp0
	req.Stream = true
	_, cacheable = cacheKey(req)
	if cacheable {
		t.Error("streaming should not be cacheable")
	}
}

func TestCacheKey_Deterministic(t *testing.T) {
	temp0 := float64(0)
	req := &ChatRequest{
		Model:       "test",
		Temperature: &temp0,
		Messages:    []ChatMessage{{Role: "user", Content: "hello"}},
	}

	key1, _ := cacheKey(req)
	key2, _ := cacheKey(req)
	if key1 != key2 {
		t.Error("same request should produce same cache key")
	}

	req.Messages[0].Content = "different"
	key3, _ := cacheKey(req)
	if key1 == key3 {
		t.Error("different content should produce different cache key")
	}
}

func TestCacheStats(t *testing.T) {
	cache := newResponseCache(100, 3600)

	cache.Set("hit", []byte("data"), 200, nil)
	cache.Get("hit")
	cache.Get("miss")

	stats := cache.Stats()
	if stats["hits"].(int64) != 1 {
		t.Errorf("expected 1 hit, got %v", stats["hits"])
	}
	if stats["misses"].(int64) != 1 {
		t.Errorf("expected 1 miss, got %v", stats["misses"])
	}
}
