package main

import "testing"

func TestParseVLLMMetrics(t *testing.T) {
	body := []byte(`# HELP vllm:num_requests_waiting Number of requests waiting.
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{model_name="qwen"} 7.0
# HELP vllm:num_requests_running Number of requests running.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model_name="qwen"} 3.0
vllm:gpu_cache_usage_perc{model_name="qwen"} 0.42
`)
	w, r := parseVLLMMetrics(body)
	if w != 7 {
		t.Errorf("waiting = %d, want 7", w)
	}
	if r != 3 {
		t.Errorf("running = %d, want 3", r)
	}
}

func TestParseVLLMMetricsNoLabels(t *testing.T) {
	body := []byte("vllm:num_requests_waiting 12\nvllm:num_requests_running 0\n")
	w, r := parseVLLMMetrics(body)
	if w != 12 || r != 0 {
		t.Errorf("got waiting=%d running=%d, want 12,0", w, r)
	}
}

func TestParseVLLMMetricsIgnoresPrefixCollision(t *testing.T) {
	// A metric that merely shares the prefix must not match.
	body := []byte("vllm:num_requests_waiting_total 999\nvllm:num_requests_waiting 4\n")
	w, _ := parseVLLMMetrics(body)
	if w != 4 {
		t.Errorf("waiting = %d, want 4 (must ignore _total collision)", w)
	}
}

func TestQueueLoad(t *testing.T) {
	b := &Backend{}
	b.QueueDepth.Store(5)
	b.ActiveInference.Store(2)
	if got := b.QueueLoad(); got != 7 {
		t.Errorf("QueueLoad = %d, want 7", got)
	}
}

func TestBackendLoadRespectsFlag(t *testing.T) {
	b := &Backend{}
	b.ActiveReqs.Store(1)
	b.QueueDepth.Store(10)
	b.ActiveInference.Store(0)
	b.QueueScraped.Store(true) // scraped vLLM node

	queueAwareRouting = false
	if got := backendLoad(b); got != 1 {
		t.Errorf("queue-aware off: backendLoad = %d, want 1 (ActiveReqs)", got)
	}
	queueAwareRouting = true
	if got := backendLoad(b); got != 10 {
		t.Errorf("queue-aware on + scraped: backendLoad = %d, want 10 (QueueLoad)", got)
	}
	queueAwareRouting = false // reset for other tests
}

func TestBackendLoadUnscrapedFallsBackToActiveReqs(t *testing.T) {
	// A non-vLLM / never-scraped backend must NOT read as idle (0) under
	// queue-aware; it falls back to its real proxy active-request count.
	b := &Backend{}
	b.ActiveReqs.Store(4)
	// QueueScraped stays false; QueueDepth/ActiveInference stay 0
	queueAwareRouting = true
	if got := backendLoad(b); got != 4 {
		t.Errorf("unscraped under queue-aware: backendLoad = %d, want 4 (ActiveReqs fallback, not 0)", got)
	}
	queueAwareRouting = false
}

func TestApplyLeastConnRRQueueAware(t *testing.T) {
	// Backend A: low proxy ActiveReqs but high inference queue.
	// Backend B: high ActiveReqs but idle inference.
	a := &Backend{Config: BackendConfig{Name: "a"}}
	a.ActiveReqs.Store(0)
	a.QueueDepth.Store(20)
	a.QueueScraped.Store(true)
	b := &Backend{Config: BackendConfig{Name: "b"}}
	b.ActiveReqs.Store(5)
	b.QueueDepth.Store(0)
	b.QueueScraped.Store(true)

	r := &Router{}
	cands := []RouteResult{{Backend: a}, {Backend: b}}

	queueAwareRouting = true
	out := r.applyLeastConnRR(append([]RouteResult{}, cands...))
	if out[0].Backend.Config.Name != "b" {
		t.Errorf("queue-aware: expected idle-inference backend 'b' first, got %q", out[0].Backend.Config.Name)
	}
	queueAwareRouting = false
	out = r.applyLeastConnRR(append([]RouteResult{}, cands...))
	if out[0].Backend.Config.Name != "a" {
		t.Errorf("queue-aware off: expected low-ActiveReqs backend 'a' first, got %q", out[0].Backend.Config.Name)
	}
}
