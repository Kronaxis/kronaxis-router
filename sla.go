package main

import (
	"sort"
	"sync"
	"time"
)

// Predictive SLA routing (ROADMAP #7). Each backend keeps a rolling window of
// recent end-to-end latencies; routing can drop or deprioritise backends whose
// p95 exceeds a rule's max_ttft_ms. Reactive today (route away from observed
// spikes); the window is the basis for a predictive policy later.

const slaWindowSize = 64 // recent samples kept per backend

// latencyWindow is a fixed-size ring of recent latencies with percentile query.
type latencyWindow struct {
	mu   sync.Mutex
	buf  [slaWindowSize]time.Duration
	n    int // total recorded (caps reads at min(n, size))
	next int
}

func (w *latencyWindow) record(d time.Duration) {
	w.mu.Lock()
	w.buf[w.next] = d
	w.next = (w.next + 1) % slaWindowSize
	w.n++
	w.mu.Unlock()
}

// percentile returns the p-th percentile (0–100) over the window, and the
// number of samples it was computed from. p95 = percentile(95).
func (w *latencyWindow) percentile(p float64) (time.Duration, int) {
	w.mu.Lock()
	count := w.n
	if count > slaWindowSize {
		count = slaWindowSize
	}
	if count == 0 {
		w.mu.Unlock()
		return 0, 0
	}
	s := make([]time.Duration, count)
	copy(s, w.buf[:count])
	w.mu.Unlock()

	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(count-1) * p / 100.0)
	if idx < 0 {
		idx = 0
	}
	if idx >= count {
		idx = count - 1
	}
	return s[idx], count
}

// slaMinSamples is the floor before SLA filtering trusts the window. Below this
// we don't have enough signal to exclude a backend.
const slaMinSamples = 8

// filterBySLA drops candidates whose p95 latency exceeds maxTTFTMs, but never
// returns empty: if every candidate is over SLA (or there isn't enough data) it
// returns the input unchanged so routing still has somewhere to go.
func filterBySLA(candidates []RouteResult, maxTTFTMs int) []RouteResult {
	if maxTTFTMs <= 0 || len(candidates) <= 1 {
		return candidates
	}
	budget := time.Duration(maxTTFTMs) * time.Millisecond
	var ok []RouteResult
	for _, c := range candidates {
		p95, samples := c.Backend.latency.percentile(95)
		if samples < slaMinSamples || p95 <= budget {
			ok = append(ok, c)
		}
	}
	if len(ok) == 0 {
		return candidates
	}
	return ok
}
