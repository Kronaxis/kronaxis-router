package main

import (
	"math"
	"os"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestGenerationCostShare(t *testing.T) {
	in := GenerationInputs{InputTokens: 1000, OutputTokens: 2000}
	rates := CostRates{GenInputUSDPer1M: 3.0, GenOutputUSDPer1M: 15.0}
	// 1000/1e6*3 + 2000/1e6*15 = 0.003 + 0.030 = 0.033
	got := GenerationCostShare(in, rates)
	if !almostEqual(got, 0.033) {
		t.Fatalf("generation cost = %v, want 0.033", got)
	}
}

func TestGenerationCostShareZeroTokens(t *testing.T) {
	got := GenerationCostShare(GenerationInputs{}, CostRates{GenInputUSDPer1M: 5, GenOutputUSDPer1M: 5})
	if !almostEqual(got, 0) {
		t.Fatalf("zero tokens should cost 0, got %v", got)
	}
}

func TestRetrievalCostShareLatencyMode(t *testing.T) {
	// 1 GiB resident, latency 3.6ms, mem rate $2/GiB-hour, compute $0.01/sec.
	in := RetrievalInputs{
		IndexBytesAttributable: 1 << 30, // exactly 1 GiB
		RetrievalLatencyUS:     3600,    // 3.6 ms
		VectorsOrNodesScanned:  500,
	}
	rates := CostRates{
		MemoryUSDPerGiBHour: 2.0,
		ComputeUSDPerSecond: 0.01,
	}
	mem, comp := RetrievalCostShare(in, rates)

	// memory: 1 GiB * $2/GiB-hr * (0.0036 sec / 3600) = 2 * 1e-6 = 2e-6
	wantMem := 2.0 * (0.0036 / 3600.0)
	if !almostEqual(mem, wantMem) {
		t.Fatalf("memory share = %v, want %v", mem, wantMem)
	}
	// compute: 0.0036 sec * $0.01 = 3.6e-5
	wantComp := 0.0036 * 0.01
	if !almostEqual(comp, wantComp) {
		t.Fatalf("compute share = %v, want %v", comp, wantComp)
	}
}

func TestRetrievalCostShareVectorMode(t *testing.T) {
	// When a per-vector rate is set it takes precedence over latency pricing.
	in := RetrievalInputs{
		VectorsOrNodesScanned: 800,
		RetrievalLatencyUS:    5000, // must be ignored for compute
	}
	rates := CostRates{ComputeUSDPerVectorScanned: 0.000002} // $2e-6 per node
	_, comp := RetrievalCostShare(in, rates)
	want := 800 * 0.000002
	if !almostEqual(comp, want) {
		t.Fatalf("vector-mode compute = %v, want %v", comp, want)
	}
}

func TestRetrievalCostShareResidencyAmortisation(t *testing.T) {
	// MemoryResidencySeconds overrides the measured latency for the memory leg.
	in := RetrievalInputs{
		IndexBytesAttributable: 2 << 30, // 2 GiB
		RetrievalLatencyUS:     100,     // tiny, must be ignored
	}
	rates := CostRates{
		MemoryUSDPerGiBHour:    3.0,
		MemoryResidencySeconds: 3600, // amortise a full resident hour to this query
	}
	mem, _ := RetrievalCostShare(in, rates)
	// 2 GiB * $3/GiB-hr * (3600/3600) = 6.0
	if !almostEqual(mem, 6.0) {
		t.Fatalf("amortised memory share = %v, want 6.0", mem)
	}
}

func TestCostAttributionTotal(t *testing.T) {
	r := RetrievalInputs{
		IndexBytesAttributable: 1 << 30,
		RetrievalLatencyUS:     3600,
		VectorsOrNodesScanned:  500,
		KReturned:              10,
	}
	g := GenerationInputs{InputTokens: 1000, OutputTokens: 2000}
	rates := CostRates{
		MemoryUSDPerGiBHour: 2.0,
		ComputeUSDPerSecond: 0.01,
		GenInputUSDPer1M:    3.0,
		GenOutputUSDPer1M:   15.0,
	}
	split := CostAttribution(r, g, rates)

	wantMem := 2.0 * (0.0036 / 3600.0)
	wantComp := 0.0036 * 0.01
	wantGen := 0.033
	wantTotal := wantMem + wantComp + wantGen

	if !almostEqual(split.RetrievalMemoryShareUSD, wantMem) {
		t.Errorf("memory share = %v, want %v", split.RetrievalMemoryShareUSD, wantMem)
	}
	if !almostEqual(split.RetrievalComputeShareUSD, wantComp) {
		t.Errorf("compute share = %v, want %v", split.RetrievalComputeShareUSD, wantComp)
	}
	if !almostEqual(split.GenerationCostUSD, wantGen) {
		t.Errorf("generation cost = %v, want %v", split.GenerationCostUSD, wantGen)
	}
	if !almostEqual(split.TotalUSD, wantTotal) {
		t.Errorf("total = %v, want %v (must equal sum of the three shares)", split.TotalUSD, wantTotal)
	}
}

func TestCostTelemetryEnabledDefaultOff(t *testing.T) {
	os.Unsetenv("KX_COST_TELEMETRY")
	if CostTelemetryEnabled() {
		t.Fatal("cost telemetry must be OFF by default")
	}
}

func TestCostTelemetryEnabledParsing(t *testing.T) {
	cases := map[string]bool{
		"1": true, "true": true, "on": true, "yes": true, "TRUE": true,
		"0": false, "off": false, "": false, "nope": false,
	}
	for v, want := range cases {
		os.Setenv("KX_COST_TELEMETRY", v)
		if got := CostTelemetryEnabled(); got != want {
			t.Errorf("KX_COST_TELEMETRY=%q -> %v, want %v", v, got, want)
		}
	}
	os.Unsetenv("KX_COST_TELEMETRY")
}

func TestTelemetryTenantResolution(t *testing.T) {
	// Header wins over service.
	meta := RouteRequest{
		Service: "svc-fallback",
		ForwardHeaders: map[string]string{
			"X-Kronaxis-Tenant-Id": "tenant-42",
			"X-Kronaxis-Vertical":  "dental",
			"X-Kronaxis-Query-Id":  "q-abc",
		},
	}
	if got := telemetryTenant(meta); got != "tenant-42" {
		t.Errorf("tenant = %q, want tenant-42", got)
	}
	if got := telemetryVertical(meta); got != "dental" {
		t.Errorf("vertical = %q, want dental", got)
	}
	if got := telemetryQueryID(meta); got != "q-abc" {
		t.Errorf("query id = %q, want q-abc", got)
	}

	// Fallback to service when headers absent.
	bare := RouteRequest{Service: "svc-only"}
	if got := telemetryTenant(bare); got != "svc-only" {
		t.Errorf("fallback tenant = %q, want svc-only", got)
	}
	if got := telemetryVertical(bare); got != "svc-only" {
		t.Errorf("fallback vertical = %q, want svc-only", got)
	}
	// No query id header -> a minted fallback id, non-empty and prefixed.
	if got := telemetryQueryID(bare); len(got) < 5 || got[:4] != "gen-" {
		t.Errorf("fallback query id = %q, want gen-* fallback", got)
	}
}

func TestEnvFloat(t *testing.T) {
	os.Setenv("KX_TEST_FLOAT", "1.5")
	if got := envFloat("KX_TEST_FLOAT", 9.9); !almostEqual(got, 1.5) {
		t.Errorf("envFloat = %v, want 1.5", got)
	}
	os.Unsetenv("KX_TEST_FLOAT")
	if got := envFloat("KX_TEST_FLOAT", 9.9); !almostEqual(got, 9.9) {
		t.Errorf("envFloat default = %v, want 9.9", got)
	}
}
