package main

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Prometheus-compatible metrics exposed at /metrics.
// Uses a simple text format (no dependency on prometheus/client_golang).

type Metrics struct {
	requestsTotal    map[string]*atomic.Int64 // label -> count
	errorsTotal      map[string]*atomic.Int64
	latencySumMS     map[string]*atomic.Int64
	latencyCount     map[string]*atomic.Int64
	latencyBuckets   map[string]*[8]atomic.Int64 // 10,25,50,100,250,500,1000,5000ms
	cacheHits        atomic.Int64
	cacheMisses      atomic.Int64
	batchSubmitted   atomic.Int64
	batchCompleted   atomic.Int64
	batchFailed      atomic.Int64
	mu               sync.RWMutex
}

var bucketBounds = [8]float64{10, 25, 50, 100, 250, 500, 1000, 5000}

var prom = &Metrics{
	requestsTotal:  make(map[string]*atomic.Int64),
	errorsTotal:    make(map[string]*atomic.Int64),
	latencySumMS:   make(map[string]*atomic.Int64),
	latencyCount:   make(map[string]*atomic.Int64),
	latencyBuckets: make(map[string]*[8]atomic.Int64),
}

func (m *Metrics) getCounter(store map[string]*atomic.Int64, key string) *atomic.Int64 {
	m.mu.RLock()
	c, ok := store[key]
	m.mu.RUnlock()
	if ok {
		return c
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := store[key]; ok {
		return c
	}
	c = &atomic.Int64{}
	store[key] = c
	return c
}

func (m *Metrics) getBuckets(key string) *[8]atomic.Int64 {
	m.mu.RLock()
	b, ok := m.latencyBuckets[key]
	m.mu.RUnlock()
	if ok {
		return b
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.latencyBuckets[key]; ok {
		return b
	}
	b = &[8]atomic.Int64{}
	m.latencyBuckets[key] = b
	return b
}

// RecordRequest records a completed request for Prometheus metrics.
func (m *Metrics) RecordRequest(service, backend, rule string, statusCode int, latency time.Duration) {
	label := fmt.Sprintf("service=%q,backend=%q,rule=%q", service, backend, rule)

	m.getCounter(m.requestsTotal, label).Add(1)
	if statusCode >= 400 {
		m.getCounter(m.errorsTotal, label).Add(1)
	}

	ms := latency.Milliseconds()
	m.getCounter(m.latencySumMS, label).Add(ms)
	m.getCounter(m.latencyCount, label).Add(1)

	// Record into the single matching bucket (handleMetrics computes cumulative sums)
	buckets := m.getBuckets(label)
	for i, bound := range bucketBounds {
		if float64(ms) <= bound {
			buckets[i].Add(1)
			break // Only increment the first matching bucket
		}
	}
}

// handleMetrics serves Prometheus text format metrics at /metrics.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// Request counters
	fmt.Fprintln(w, "# HELP kronaxis_router_requests_total Total requests by service, backend, and rule.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_requests_total counter")
	prom.mu.RLock()
	keys := sortedKeys(prom.requestsTotal)
	for _, label := range keys {
		fmt.Fprintf(w, "kronaxis_router_requests_total{%s} %d\n", label, prom.requestsTotal[label].Load())
	}

	// Error counters
	fmt.Fprintln(w, "# HELP kronaxis_router_errors_total Error responses (4xx/5xx) by label.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_errors_total counter")
	keys = sortedKeys(prom.errorsTotal)
	for _, label := range keys {
		fmt.Fprintf(w, "kronaxis_router_errors_total{%s} %d\n", label, prom.errorsTotal[label].Load())
	}

	// Latency histogram
	fmt.Fprintln(w, "# HELP kronaxis_router_request_duration_ms Request latency in milliseconds.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_request_duration_ms histogram")
	for _, label := range sortedKeys(prom.latencyBuckets) {
		buckets := prom.latencyBuckets[label]
		cumulative := int64(0)
		for i, bound := range bucketBounds {
			cumulative += buckets[i].Load()
			fmt.Fprintf(w, "kronaxis_router_request_duration_ms_bucket{%s,le=\"%.0f\"} %d\n", label, bound, cumulative)
		}
		fmt.Fprintf(w, "kronaxis_router_request_duration_ms_bucket{%s,le=\"+Inf\"} %d\n", label, prom.latencyCount[label].Load())
		fmt.Fprintf(w, "kronaxis_router_request_duration_ms_sum{%s} %d\n", label, prom.latencySumMS[label].Load())
		fmt.Fprintf(w, "kronaxis_router_request_duration_ms_count{%s} %d\n", label, prom.latencyCount[label].Load())
	}
	prom.mu.RUnlock()

	// Cache metrics
	fmt.Fprintln(w, "# HELP kronaxis_router_cache_hits_total Cache hits.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_cache_hits_total counter")
	fmt.Fprintf(w, "kronaxis_router_cache_hits_total %d\n", prom.cacheHits.Load())
	fmt.Fprintln(w, "# HELP kronaxis_router_cache_misses_total Cache misses.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_cache_misses_total counter")
	fmt.Fprintf(w, "kronaxis_router_cache_misses_total %d\n", prom.cacheMisses.Load())

	// Batch metrics
	fmt.Fprintln(w, "# HELP kronaxis_router_batch_submitted_total Batch jobs submitted.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_batch_submitted_total counter")
	fmt.Fprintf(w, "kronaxis_router_batch_submitted_total %d\n", prom.batchSubmitted.Load())
	fmt.Fprintln(w, "# HELP kronaxis_router_batch_completed_total Batch jobs completed.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_batch_completed_total counter")
	fmt.Fprintf(w, "kronaxis_router_batch_completed_total %d\n", prom.batchCompleted.Load())
	fmt.Fprintln(w, "# HELP kronaxis_router_batch_failed_total Batch jobs failed.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_batch_failed_total counter")
	fmt.Fprintf(w, "kronaxis_router_batch_failed_total %d\n", prom.batchFailed.Load())

	// Backend health gauges
	fmt.Fprintln(w, "# HELP kronaxis_router_backend_healthy Whether a backend is healthy (1=yes, 0=no).")
	fmt.Fprintln(w, "# TYPE kronaxis_router_backend_healthy gauge")
	pool.mu.RLock()
	for _, b := range pool.backends {
		b.mu.RLock()
		healthy := 0
		if b.Status == StatusHealthy {
			healthy = 1
		}
		fmt.Fprintf(w, "kronaxis_router_backend_healthy{backend=%q,type=%q} %d\n", b.Config.Name, b.Config.Type, healthy)
		fmt.Fprintf(w, "kronaxis_router_backend_active_requests{backend=%q,type=%q} %d\n", b.Config.Name, b.Config.Type, b.ActiveReqs.Load())
		b.mu.RUnlock()
	}
	pool.mu.RUnlock()

	// Uptime
	fmt.Fprintln(w, "# HELP kronaxis_router_uptime_seconds Uptime in seconds.")
	fmt.Fprintln(w, "# TYPE kronaxis_router_uptime_seconds gauge")
	fmt.Fprintf(w, "kronaxis_router_uptime_seconds %d\n", int(time.Since(startupTime).Seconds()))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
