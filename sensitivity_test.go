package main

import (
	"testing"
)

func TestScoreSensitivity_HighPII(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Please update the record for John Smith, NI number QQ 12 34 56 C, " +
				"email john.smith@example.co.uk, and note his medical diagnosis in the file."},
		},
	}
	s := float64(ScoreSensitivity(req))
	if s < SensitiveFloor {
		t.Errorf("PII-heavy prompt should score >= %.0f, got %.1f", SensitiveFloor, s)
	}
}

func TestScoreSensitivity_Credentials(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Here is my password: hunter2 and the api key sk-abc123, store them."},
		},
	}
	s := float64(ScoreSensitivity(req))
	if s < SensitiveFloor {
		t.Errorf("credential prompt should score >= %.0f, got %.1f", SensitiveFloor, s)
	}
}

func TestScoreSensitivity_Financial(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Pay the invoice from account 12345678, sort code 09-01-27, card 4111 1111 1111 1111."},
		},
	}
	s := float64(ScoreSensitivity(req))
	if s < SensitiveFloor {
		t.Errorf("financial prompt should score >= %.0f, got %.1f", SensitiveFloor, s)
	}
}

func TestScoreSensitivity_Benign(t *testing.T) {
	for _, prompt := range []string{
		"Tell me about cats.",
		"Summarise the plot of Hamlet in two sentences.",
		"Classify this review as positive or negative: 'great product'.",
		"What is the capital of France?",
	} {
		req := &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: prompt}}}
		s := float64(ScoreSensitivity(req))
		if s >= SensitiveFloor {
			t.Errorf("benign prompt %q should score below %.0f, got %.1f", prompt, SensitiveFloor, s)
		}
	}
}

func TestMustStayTrusted_GateOffByDefault(t *testing.T) {
	t.Setenv("KX_SENSITIVITY_ROUTING", "")
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "NI number QQ 12 34 56 C, medical record, password: hunter2"},
		},
	}
	if MustStayTrusted(req) {
		t.Error("gate must be OFF by default: sensitive prompt should not force trusted routing")
	}
}

func TestMustStayTrusted_GateOn(t *testing.T) {
	t.Setenv("KX_SENSITIVITY_ROUTING", "1")
	sensitive := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Update John Smith, NI number QQ 12 34 56 C, medical diagnosis, sort code 09-01-27."},
		},
	}
	if !MustStayTrusted(sensitive) {
		t.Error("gate ON + sensitive prompt should force trusted routing")
	}
	benign := &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "Tell me about cats."}}}
	if MustStayTrusted(benign) {
		t.Error("gate ON + benign prompt should NOT force trusted routing")
	}
}

func TestClassifyScore_Band(t *testing.T) {
	cases := []struct {
		score float64
		want  SensitivityDecision
	}{
		{0, RouteFree},
		{44.9, RouteFree},
		{SensitiveFloor - AmbiguityBand, TrustedUncertain}, // 45.0 lower edge, inclusive
		{50, TrustedUncertain},
		{54.9, TrustedUncertain},
		{SensitiveFloor, TrustedSensitive}, // 55.0 floor, inclusive
		{80, TrustedSensitive},
		{100, TrustedSensitive},
	}
	for _, c := range cases {
		if got := classifyScore(c.score); got != c.want {
			t.Errorf("classifyScore(%.1f) = %v, want %v", c.score, got, c.want)
		}
	}
}

func TestMustStayTrusted_AmbiguityFailsClosed(t *testing.T) {
	// A grey-band score must fail closed to trusted when the gate is on, and be
	// distinct from a clear sensitive hit.
	t.Setenv("KX_SENSITIVITY_ROUTING", "1")
	if classifyScore(50) != TrustedUncertain {
		t.Fatalf("50 should be TrustedUncertain")
	}
	// gate off: even a grey-band or sensitive score must not force routing
	t.Setenv("KX_SENSITIVITY_ROUTING", "")
	req := &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "diagnosis and prescription notes"}}}
	if MustStayTrusted(req) {
		t.Error("gate OFF must never force trusted routing regardless of score")
	}
}

func TestRoutingSavings(t *testing.T) {
	// ComplianceGate default ratios; a 60/25/15 skew toward cheap tiers.
	dist := []TierCostRatio{
		{"small", 0.60, 0.15},
		{"mid", 0.25, 0.26},
		{"large", 0.15, 1.00},
	}
	s := RoutingSavings(dist)
	// 1 - (0.6*0.15 + 0.25*0.26 + 0.15*1.0) = 1 - (0.09+0.065+0.15) = 0.695
	if s < 0.69 || s > 0.70 {
		t.Errorf("RoutingSavings = %.4f, want ~0.695", s)
	}
	// everything to the top tier = zero saving
	if got := RoutingSavings([]TierCostRatio{{"large", 1.0, 1.0}}); got != 0 {
		t.Errorf("all-top-tier savings = %.4f, want 0", got)
	}
}
