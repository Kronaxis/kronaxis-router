package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Queue-aware load balancing (ROADMAP Phase 1, complements KV pinning).
//
// Health checks tell you a backend is alive, not whether it is busy. The
// QueueScraper periodically reads each vLLM backend's /metrics endpoint and
// records two Prometheus counters onto the Backend:
//   - vllm:num_requests_waiting  -> QueueDepth      (queued, not yet running)
//   - vllm:num_requests_running  -> ActiveInference (currently generating)
//
// When server.queue_aware_routing is enabled, the router's balancing prefers
// the candidate with the lowest QueueLoad() (= QueueDepth + ActiveInference),
// so traffic flows to the least-loaded node — and, stacked with KV pinning, to
// the warmest cache unless it is overloaded. Best-effort: a scrape failure just
// leaves the previous values in place and never affects request handling.

// QueueScraper polls vLLM backends' /metrics on an interval.
type QueueScraper struct {
	pool     *BackendPool
	interval time.Duration
	client   *http.Client
}

func newQueueScraper(pool *BackendPool, interval time.Duration) *QueueScraper {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &QueueScraper{
		pool:     pool,
		interval: interval,
		client:   &http.Client{Timeout: 3 * time.Second},
	}
}

// Run scrapes once immediately, then every interval until ctx is cancelled.
func (q *QueueScraper) Run(ctx context.Context) {
	q.scrapeAll(ctx)
	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.scrapeAll(ctx)
		}
	}
}

func (q *QueueScraper) scrapeAll(ctx context.Context) {
	for _, b := range q.pool.vllmBackends() {
		waiting, running, err := q.scrape(ctx, b.Config.URL)
		if err != nil {
			// Best-effort: keep last-known values, don't disturb routing.
			continue
		}
		b.QueueDepth.Store(waiting)
		b.ActiveInference.Store(running)
		b.QueueScraped.Store(true)
	}
}

func (q *QueueScraper) scrape(ctx context.Context, baseURL string) (waiting, running int64, err error) {
	url := strings.TrimRight(baseURL, "/") + "/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("metrics HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // cap at 1MB
	if err != nil {
		return 0, 0, err
	}
	w, r := parseVLLMMetrics(body)
	return w, r, nil
}

// parseVLLMMetrics extracts the two queue counters from Prometheus text. vLLM
// emits them with a label set and a float value, e.g.:
//
//	vllm:num_requests_waiting{model_name="..."} 3.0
//
// so we match the metric prefix and take the last whitespace-separated field
// as a float, truncating to int.
func parseVLLMMetrics(body []byte) (waiting, running int64) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if v, ok := metricValue(line, "vllm:num_requests_waiting"); ok {
			waiting = v
		} else if v, ok := metricValue(line, "vllm:num_requests_running"); ok {
			running = v
		}
	}
	return waiting, running
}

// metricValue returns the integer value of a Prometheus line whose metric name
// is `name` (with or without a {label} set), and whether it matched.
func metricValue(line, name string) (int64, bool) {
	if !strings.HasPrefix(line, name) {
		return 0, false
	}
	// The char right after the name must be '{' (labels) or ' ' (no labels),
	// otherwise this is a different metric sharing the prefix.
	rest := line[len(name):]
	if len(rest) == 0 || (rest[0] != '{' && rest[0] != ' ') {
		return 0, false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscanf(fields[len(fields)-1], "%g", &f); err != nil {
		return 0, false
	}
	if f < 0 {
		f = 0
	}
	return int64(f), true
}
