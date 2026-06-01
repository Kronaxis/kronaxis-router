package main

import (
	"testing"
	"time"
)

// ---- #7 predictive SLA ----

func TestLatencyWindowPercentile(t *testing.T) {
	var w latencyWindow
	for i := 1; i <= 100; i++ {
		w.record(time.Duration(i) * time.Millisecond)
	}
	// window only holds the last slaWindowSize samples (37..100 for size 64).
	p95, n := w.percentile(95)
	if n != slaWindowSize {
		t.Errorf("samples = %d, want %d", n, slaWindowSize)
	}
	// p95 of 37..100 should be near the top of the range.
	if p95 < 90*time.Millisecond || p95 > 100*time.Millisecond {
		t.Errorf("p95 = %v, want ~95ms", p95)
	}
	if _, n0 := (&latencyWindow{}).percentile(95); n0 != 0 {
		t.Error("empty window should report 0 samples")
	}
}

func TestFilterBySLA(t *testing.T) {
	fast := &Backend{Config: BackendConfig{Name: "fast"}}
	slow := &Backend{Config: BackendConfig{Name: "slow"}}
	for i := 0; i < slaMinSamples+2; i++ {
		fast.latency.record(100 * time.Millisecond)
		slow.latency.record(5 * time.Second)
	}
	cands := []RouteResult{{Backend: fast}, {Backend: slow}}

	// Budget 1s: slow (p95 5s) dropped, fast kept.
	got := filterBySLA(cands, 1000)
	if len(got) != 1 || got[0].Backend.Config.Name != "fast" {
		t.Errorf("expected only 'fast' under 1s SLA; got %+v", got)
	}
	// maxTTFTMs 0 → no filtering.
	if len(filterBySLA(cands, 0)) != 2 {
		t.Error("0 budget should not filter")
	}
	// Both over SLA → never empty (returns input).
	if len(filterBySLA(cands, 10)) != 2 {
		t.Error("when all over SLA, must return all rather than empty")
	}
}

func TestFilterBySLAInsufficientSamples(t *testing.T) {
	b := &Backend{Config: BackendConfig{Name: "new"}}
	b.latency.record(9 * time.Second) // 1 sample < slaMinSamples
	cands := []RouteResult{{Backend: b}, {Backend: &Backend{Config: BackendConfig{Name: "x"}}}}
	if len(filterBySLA(cands, 100)) != 2 {
		t.Error("backends below slaMinSamples must not be filtered out")
	}
}

// ---- #14 spot-market arbitrage ----

func TestParsePriceFeed(t *testing.T) {
	m, err := parsePriceFeed([]byte(`{"cloud-fast":{"input_1m":2.5,"output_1m":10},"local":{"input_1m":0.01,"output_1m":0.01}}`))
	if err != nil {
		t.Fatal(err)
	}
	if m["cloud-fast"].Input1M != 2.5 || m["local"].Input1M != 0.01 {
		t.Errorf("parsed prices wrong: %+v", m)
	}
}

func TestEffectiveCost(t *testing.T) {
	b := &Backend{Config: BackendConfig{Name: "b1", CostInput1M: 5.0}}
	// No override → config cost.
	if b.EffectiveCost() != 5.0 {
		t.Errorf("EffectiveCost = %v, want 5.0 (config)", b.EffectiveCost())
	}
	// Override wins.
	priceMu.Lock()
	priceOverrides = map[string]backendPrice{"b1": {Input1M: 1.25}}
	priceMu.Unlock()
	if b.EffectiveCost() != 1.25 {
		t.Errorf("EffectiveCost = %v, want 1.25 (live override)", b.EffectiveCost())
	}
	priceMu.Lock()
	priceOverrides = map[string]backendPrice{}
	priceMu.Unlock()
}

// ---- header parsing (#15/#18 opt-in) ----

func TestIsTruthyHeader(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "Yes"} {
		if !isTruthyHeader(v) {
			t.Errorf("%q should be truthy", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off"} {
		if isTruthyHeader(v) {
			t.Errorf("%q should be falsy", v)
		}
	}
}
