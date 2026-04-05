package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// LiveStats tracks real-time request metrics for the dashboard.
type LiveStats struct {
	TotalRequests   int64            `json:"total_requests"`
	ActiveRequests  int64            `json:"active_requests"`
	TotalErrors     int64            `json:"total_errors"`
	AvgLatencyMS    float64          `json:"avg_latency_ms"`
	RequestsByRule  map[string]int64 `json:"requests_by_rule"`
	RequestsBySvc   map[string]int64 `json:"requests_by_service"`
	RequestsByModel map[string]int64 `json:"requests_by_model"`
	UptimeSeconds   int64            `json:"uptime_seconds"`
}

var (
	statsMu   sync.RWMutex
	liveStats = LiveStats{
		RequestsByRule:  make(map[string]int64),
		RequestsBySvc:   make(map[string]int64),
		RequestsByModel: make(map[string]int64),
	}
	totalLatencyNS atomic.Int64
	activeReqs     atomic.Int64
)

func recordStat(meta RouteRequest, route RouteResult, latency time.Duration, success bool) {
	statsMu.Lock()
	defer statsMu.Unlock()

	liveStats.TotalRequests++
	if !success {
		liveStats.TotalErrors++
	}

	totalLatencyNS.Add(latency.Nanoseconds())
	if liveStats.TotalRequests > 0 {
		liveStats.AvgLatencyMS = float64(totalLatencyNS.Load()) / float64(liveStats.TotalRequests) / 1e6
	}

	ruleName := ""
	if route.Rule != nil {
		ruleName = route.Rule.Name
		liveStats.RequestsByRule[ruleName]++
	}
	svc := meta.Service
	if svc == "" {
		svc = "unknown"
	}
	liveStats.RequestsBySvc[svc]++

	backendName := ""
	if route.Backend != nil {
		backendName = route.Backend.Config.Name
		liveStats.RequestsByModel[backendName]++
	}

	liveStats.UptimeSeconds = int64(time.Since(startupTime).Seconds())

	// Record Prometheus metrics
	statusCode := 200
	if !success {
		statusCode = 500
	}
	prom.RecordRequest(svc, backendName, ruleName, statusCode, latency)
}

func incActive()  { activeReqs.Add(1) }
func decActive()  { activeReqs.Add(-1) }
