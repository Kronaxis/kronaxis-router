package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSynth_AddBackend_AndCreateRule_OnEmpty(t *testing.T) {
	p := writeYAML(t, `server:
  port: 8050
backends: []
rules: []
`)
	in := AgentSynthInput{
		Name: "codex-cli", GatewayURL: "http://localhost:8055", Model: "codex-cli",
		Tier: 1, CostClass: "premium",
		Capabilities: []string{"agentic", "file-edit"},
	}
	if err := SynthAgentBackend(p, in); err != nil {
		t.Fatal(err)
	}
	out := read(t, p)
	if !strings.Contains(out, "name: codex-cli") {
		t.Errorf("backend stanza missing:\n%s", out)
	}
	if !strings.Contains(out, "tier-1-auto") {
		t.Errorf("auto rule missing:\n%s", out)
	}
	// Idempotent: re-register doesn't duplicate.
	if err := SynthAgentBackend(p, in); err != nil {
		t.Fatal(err)
	}
	out2 := read(t, p)
	count := strings.Count(out2, "name: codex-cli")
	if count != 1 {
		t.Errorf("expected exactly 1 codex-cli stanza, got %d", count)
	}
}

func TestSynth_AppendsToExistingTierRule(t *testing.T) {
	p := writeYAML(t, `server:
  port: 8050
backends:
  - name: existing
    url: "http://localhost:9000"
    type: openai
rules:
  - name: tier-2-rule
    priority: 50
    match:
      tier: 2
    backends: [existing]
`)
	in := AgentSynthInput{
		Name: "aider", GatewayURL: "http://localhost:8055", Model: "aider",
		Tier: 2, CostClass: "standard",
	}
	if err := SynthAgentBackend(p, in); err != nil {
		t.Fatal(err)
	}
	out := read(t, p)
	if !strings.Contains(out, "name: aider") {
		t.Errorf("aider stanza missing")
	}
	// Tier-2-rule should now contain aider (no new tier-2-auto rule created).
	if strings.Contains(out, "tier-2-auto") {
		t.Errorf("should not have created auto rule when existing tier-2 rule present")
	}
	if !strings.Contains(out, "tier-2-rule") {
		t.Errorf("existing rule disappeared")
	}
}

func TestSynth_RemoveStripsBackendAndRuleRefs(t *testing.T) {
	p := writeYAML(t, `server:
  port: 8050
backends: []
rules: []
`)
	in := AgentSynthInput{Name: "gemini-cli", GatewayURL: "http://localhost:8055", Tier: 2}
	if err := SynthAgentBackend(p, in); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAgentBackend(p, "gemini-cli"); err != nil {
		t.Fatal(err)
	}
	out := read(t, p)
	if strings.Contains(out, "name: gemini-cli") {
		t.Errorf("backend stanza still present after remove:\n%s", out)
	}
}

func TestSynth_PreservesUnrelatedKeys(t *testing.T) {
	p := writeYAML(t, `server:
  port: 8050
  default_timeout: 120s
backends:
  - name: keepme
    url: "http://localhost:9001"
    type: vllm
budgets:
  default:
    daily_limit_usd: 5.0
rules: []
`)
	in := AgentSynthInput{Name: "x", GatewayURL: "http://localhost:8055", Tier: 3}
	if err := SynthAgentBackend(p, in); err != nil {
		t.Fatal(err)
	}
	out := read(t, p)
	for _, want := range []string{"keepme", "default_timeout", "daily_limit_usd: 5"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q to survive synth, output:\n%s", want, out)
		}
	}
}
