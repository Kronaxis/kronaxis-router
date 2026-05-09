package main

import (
	"testing"
)

func TestBackendPool_GetHealthy(t *testing.T) {
	bp := newBackendPool([]BackendConfig{
		{Name: "a", URL: "http://a", Type: "vllm", Capabilities: []string{"json_output"}, MaxConcurrent: 10},
		{Name: "b", URL: "http://b", Type: "vllm", Capabilities: []string{"vision"}, MaxConcurrent: 10},
		{Name: "c", URL: "http://c", Type: "vllm", Capabilities: []string{"json_output", "vision"}, MaxConcurrent: 10},
	})

	// All healthy, no capability filter
	healthy := bp.GetHealthy([]string{"a", "b", "c"}, nil)
	if len(healthy) != 3 {
		t.Fatalf("expected 3 healthy, got %d", len(healthy))
	}

	// Filter by capability
	healthy = bp.GetHealthy([]string{"a", "b", "c"}, []string{"vision"})
	if len(healthy) != 2 {
		t.Fatalf("expected 2 with vision, got %d", len(healthy))
	}

	// Mark one as down
	bp.backends["b"].Status = StatusDown
	healthy = bp.GetHealthy([]string{"a", "b", "c"}, []string{"vision"})
	if len(healthy) != 1 {
		t.Fatalf("expected 1 (c only), got %d", len(healthy))
	}
	if healthy[0].Config.Name != "c" {
		t.Errorf("expected c, got %s", healthy[0].Config.Name)
	}
}

func TestBackendPool_GetWithAdapter(t *testing.T) {
	bp := newBackendPool([]BackendConfig{
		{Name: "base", URL: "http://base", Type: "vllm", MaxConcurrent: 10},
		{Name: "lora", URL: "http://lora", Type: "vllm", MaxConcurrent: 10, LoRAAdapters: []string{"sdr", "closer"}},
	})

	// Find backend with sdr adapter
	with := bp.GetWithAdapter([]string{"base", "lora"}, "sdr")
	if len(with) != 1 {
		t.Fatalf("expected 1 with sdr, got %d", len(with))
	}
	if with[0].Config.Name != "lora" {
		t.Errorf("expected lora, got %s", with[0].Config.Name)
	}

	// No backend has this adapter
	with = bp.GetWithAdapter([]string{"base", "lora"}, "nonexistent")
	if len(with) != 0 {
		t.Fatalf("expected 0, got %d", len(with))
	}
}

func TestBackend_HasCapability(t *testing.T) {
	b := &Backend{Config: BackendConfig{Capabilities: []string{"json_output", "vision"}}}

	if !b.HasCapability("vision") {
		t.Error("should have vision")
	}
	if b.HasCapability("lora_adapter") {
		t.Error("should not have lora_adapter")
	}
}

func TestBackend_IsAvailable(t *testing.T) {
	b := &Backend{Config: BackendConfig{MaxConcurrent: 2}, Status: StatusHealthy}

	if !b.IsAvailable() {
		t.Error("healthy backend should be available")
	}

	b.Status = StatusDown
	if b.IsAvailable() {
		t.Error("down backend should not be available")
	}

	b.Status = StatusDegraded
	if !b.IsAvailable() {
		t.Error("degraded backend should be available")
	}
}

func TestBackendPool_RegisterDeregister(t *testing.T) {
	bp := newBackendPool(nil)

	bp.Register(BackendConfig{Name: "dynamic", URL: "http://dynamic", Type: "vllm", Dynamic: true})
	if bp.Get("dynamic") == nil {
		t.Error("registered backend should exist")
	}

	bp.Deregister("dynamic")
	if bp.Get("dynamic") != nil {
		t.Error("deregistered backend should not exist")
	}
}
