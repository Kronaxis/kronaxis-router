package main

import (
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics holds in-memory counters/gauges. We avoid the prometheus client
// dependency and emit Prometheus text-format directly to keep deps minimal.
// Schema is stable; scrape with any Prometheus-compatible collector.
type Metrics struct {
	mu sync.Mutex

	requestsTotal      map[labelKey]uint64
	requestErrorsTotal map[labelKey]uint64
	requestDurationMS  map[labelKey]*histogram
	toolCallsTotal     map[string]uint64
	costUSDTotal       map[string]float64
	turnsTotal         map[string]uint64

	activeRequests atomic.Int64
	startedAt      time.Time
}

type labelKey struct {
	Adapter string
	Status  string // "ok", "error", "stub"
	Model   string
}

func newMetrics() *Metrics {
	return &Metrics{
		requestsTotal:      map[labelKey]uint64{},
		requestErrorsTotal: map[labelKey]uint64{},
		requestDurationMS:  map[labelKey]*histogram{},
		toolCallsTotal:     map[string]uint64{},
		costUSDTotal:       map[string]float64{},
		turnsTotal:         map[string]uint64{},
		startedAt:          time.Now(),
	}
}

func (m *Metrics) RequestStarted() {
	m.activeRequests.Add(1)
}

func (m *Metrics) RequestFinished(adapter, model, status string, durationMS int64, costUSD float64, turns int) {
	m.activeRequests.Add(-1)
	m.mu.Lock()
	defer m.mu.Unlock()
	k := labelKey{Adapter: adapter, Status: status, Model: model}
	m.requestsTotal[k]++
	if status == "error" {
		m.requestErrorsTotal[k]++
	}
	h, ok := m.requestDurationMS[k]
	if !ok {
		h = newHistogram([]float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000})
		m.requestDurationMS[k] = h
	}
	h.observe(float64(durationMS))
	m.costUSDTotal[adapter] += costUSD
	m.turnsTotal[adapter] += uint64(turns)
}

func (m *Metrics) ToolInvoked(toolName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCallsTotal[toolName]++
}

// histogram is a minimal cumulative-bucket histogram in the Prometheus shape.
type histogram struct {
	buckets []float64
	counts  []uint64
	sum     float64
	total   uint64
}

func newHistogram(buckets []float64) *histogram {
	return &histogram{buckets: buckets, counts: make([]uint64, len(buckets))}
}

func (h *histogram) observe(v float64) {
	h.sum += v
	h.total++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

func (m *Metrics) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		writeHELP(w, "agent_gateway_requests_total", "counter", "Total agent gateway requests")
		for k, v := range m.requestsTotal {
			writeMetric(w, "agent_gateway_requests_total",
				labels{"adapter": k.Adapter, "model": k.Model, "status": k.Status}, float64(v))
		}

		writeHELP(w, "agent_gateway_request_errors_total", "counter", "Total errored requests")
		for k, v := range m.requestErrorsTotal {
			writeMetric(w, "agent_gateway_request_errors_total",
				labels{"adapter": k.Adapter, "model": k.Model, "status": k.Status}, float64(v))
		}

		writeHELP(w, "agent_gateway_request_duration_ms", "histogram", "Request wall-clock duration in milliseconds")
		for k, h := range m.requestDurationMS {
			lab := labels{"adapter": k.Adapter, "model": k.Model, "status": k.Status}
			for i, b := range h.buckets {
				lb := lab.with("le", strconv.FormatFloat(b, 'f', -1, 64))
				writeMetric(w, "agent_gateway_request_duration_ms_bucket", lb, float64(h.counts[i]))
			}
			lb := lab.with("le", "+Inf")
			writeMetric(w, "agent_gateway_request_duration_ms_bucket", lb, float64(h.total))
			writeMetric(w, "agent_gateway_request_duration_ms_sum", lab, h.sum)
			writeMetric(w, "agent_gateway_request_duration_ms_count", lab, float64(h.total))
		}

		writeHELP(w, "agent_gateway_tool_calls_total", "counter", "Total tool calls observed")
		for tool, v := range m.toolCallsTotal {
			writeMetric(w, "agent_gateway_tool_calls_total", labels{"tool": tool}, float64(v))
		}

		writeHELP(w, "agent_gateway_cost_usd_total", "counter", "Total cumulative cost in USD")
		for adapter, v := range m.costUSDTotal {
			writeMetric(w, "agent_gateway_cost_usd_total", labels{"adapter": adapter}, v)
		}

		writeHELP(w, "agent_gateway_turns_total", "counter", "Total agent turns")
		for adapter, v := range m.turnsTotal {
			writeMetric(w, "agent_gateway_turns_total", labels{"adapter": adapter}, float64(v))
		}

		writeHELP(w, "agent_gateway_active_requests", "gauge", "Currently in-flight requests")
		writeMetric(w, "agent_gateway_active_requests", nil, float64(m.activeRequests.Load()))

		writeHELP(w, "agent_gateway_uptime_seconds", "gauge", "Seconds since start")
		writeMetric(w, "agent_gateway_uptime_seconds", nil, time.Since(m.startedAt).Seconds())
	}
}

type labels map[string]string

func (l labels) with(k, v string) labels {
	out := make(labels, len(l)+1)
	for kk, vv := range l {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func writeHELP(w http.ResponseWriter, name, kind, help string) {
	_, _ = w.Write([]byte("# HELP " + name + " " + help + "\n"))
	_, _ = w.Write([]byte("# TYPE " + name + " " + kind + "\n"))
}

func writeMetric(w http.ResponseWriter, name string, l labels, v float64) {
	_, _ = w.Write([]byte(name))
	if len(l) > 0 {
		_, _ = w.Write([]byte("{"))
		first := true
		for k, val := range l {
			if !first {
				_, _ = w.Write([]byte(","))
			}
			first = false
			_, _ = w.Write([]byte(k + "=\"" + escapeLabel(val) + "\""))
		}
		_, _ = w.Write([]byte("}"))
	}
	_, _ = w.Write([]byte(" " + strconv.FormatFloat(v, 'f', -1, 64) + "\n"))
}

func escapeLabel(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, []byte(string(r))...)
		}
	}
	return string(out)
}
