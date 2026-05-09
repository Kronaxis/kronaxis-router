package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// AgentSynthInput is the small bundle of fields the synthesiser needs to
// add a CLI agent as a routable backend. Sourced from the agent-gateway's
// /v1/agents/<name> endpoint or supplied by the user on the CLI.
type AgentSynthInput struct {
	Name         string   // backend stanza name (matches profile name)
	GatewayURL   string   // base URL of agent-gateway, e.g. http://localhost:8055
	Model        string   // OpenAI model string (the profile name; submodel passed at request time)
	Tier         int      // routing tier
	CostClass    string   // cheap | standard | premium (chain ordering only)
	Capabilities []string // metadata; not consumed by current rule engine
}

// SynthAgentBackend updates configPath in-place to:
//  1. Upsert a backend stanza for the agent (idempotent by name).
//  2. Append the backend to the routing rule whose match.tier == in.Tier.
//     Creates the rule (named "tier-<n>-auto") if absent.
//
// Writes are atomic: temp file + rename. Returns nil on success or an error
// describing what went wrong (no partial writes).
func SynthAgentBackend(configPath string, in AgentSynthInput) error {
	if in.Name == "" {
		return fmt.Errorf("synth: Name is required")
	}
	if in.GatewayURL == "" {
		in.GatewayURL = "http://localhost:8055"
	}
	if in.Model == "" {
		in.Model = in.Name
	}
	if in.Tier <= 0 {
		in.Tier = 2
	}
	if in.CostClass == "" {
		in.CostClass = "standard"
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}

	// Decode into a yaml.Node so comments and unknown keys are preserved.
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	doc := documentRoot(&root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a mapping")
	}

	upsertBackendStanza(doc, in)
	upsertRuleEntry(doc, in)

	// Re-marshal preserving the document.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	_ = enc.Close()

	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// RemoveAgentBackend removes the backend stanza named `name` and strips it
// from any rule's backends chain. No-op if not present.
func RemoveAgentBackend(configPath, name string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return err
	}
	doc := documentRoot(&root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a mapping")
	}

	removeBackendStanza(doc, name)
	removeFromAllRules(doc, name)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return err
	}
	_ = enc.Close()
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath)
}

// ----- internals -----

// documentRoot returns the actual mapping node when given a DocumentNode.
func documentRoot(root *yaml.Node) *yaml.Node {
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		return root.Content[0]
	}
	return root
}

func findField(mapping *yaml.Node, key string) (int, *yaml.Node) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return -1, nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return i, v
		}
	}
	return -1, nil
}

func ensureSequence(mapping *yaml.Node, key string) *yaml.Node {
	_, val := findField(mapping, key)
	if val != nil && val.Kind == yaml.SequenceNode {
		return val
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	if val != nil {
		// Replace mismatched type.
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				mapping.Content[i+1] = seq
				return seq
			}
		}
	}
	mapping.Content = append(mapping.Content, keyNode, seq)
	return seq
}

func upsertBackendStanza(doc *yaml.Node, in AgentSynthInput) {
	backends := ensureSequence(doc, "backends")
	// Look up existing stanza by name.
	for _, item := range backends.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		_, n := findField(item, "name")
		if n != nil && n.Value == in.Name {
			updateBackendStanza(item, in)
			return
		}
	}
	// Append new.
	stanza := buildBackendStanza(in)
	backends.Content = append(backends.Content, stanza)
}

func buildBackendStanza(in AgentSynthInput) *yaml.Node {
	stanza := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k, v string, style yaml.Style) {
		stanza.Content = append(stanza.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: style},
		)
	}
	add("name", in.Name, 0)
	add("url", in.GatewayURL, yaml.DoubleQuotedStyle)
	// We use type: openai because the agent-gateway exposes a fully
	// OpenAI-compatible /v1/chat/completions surface; a distinct
	// type: agent-gateway is reserved for future divergence.
	add("type", "openai", 0)
	add("model_name", in.Model, yaml.DoubleQuotedStyle)
	stanza.Content = append(stanza.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "cost_input_1m"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "0.00", Tag: "!!float"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "cost_output_1m"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "0.00", Tag: "!!float"},
	)
	if len(in.Capabilities) > 0 {
		caps := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, c := range in.Capabilities {
			caps.Content = append(caps.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: c})
		}
		stanza.Content = append(stanza.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "capabilities"}, caps)
	}
	stanza.Content = append(stanza.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "max_concurrent"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "4", Tag: "!!int"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "health_endpoint"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "/healthz", Style: yaml.DoubleQuotedStyle},
	)
	// Stash agent metadata as a subtree the rule engine ignores but humans
	// (and future rule code) can read.
	meta := &yaml.Node{Kind: yaml.MappingNode}
	meta.Content = append(meta.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "tier"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", in.Tier), Tag: "!!int"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "cost_class"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: in.CostClass},
	)
	stanza.Content = append(stanza.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "agent_metadata"}, meta)
	return stanza
}

func updateBackendStanza(stanza *yaml.Node, in AgentSynthInput) {
	setScalar(stanza, "url", in.GatewayURL, yaml.DoubleQuotedStyle)
	setScalar(stanza, "model_name", in.Model, yaml.DoubleQuotedStyle)
	if len(in.Capabilities) > 0 {
		setCaps(stanza, in.Capabilities)
	}
	// Update agent_metadata
	_, meta := findField(stanza, "agent_metadata")
	if meta == nil {
		meta = &yaml.Node{Kind: yaml.MappingNode}
		stanza.Content = append(stanza.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "agent_metadata"}, meta)
	}
	setScalar(meta, "tier", fmt.Sprintf("%d", in.Tier), 0)
	setScalar(meta, "cost_class", in.CostClass, 0)
}

func setScalar(parent *yaml.Node, key, value string, style yaml.Style) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1].Kind = yaml.ScalarNode
			parent.Content[i+1].Value = value
			parent.Content[i+1].Style = style
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Style: style},
	)
}

func setCaps(parent *yaml.Node, caps []string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == "capabilities" {
			seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, c := range caps {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: c})
			}
			parent.Content[i+1] = seq
			return
		}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, c := range caps {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: c})
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "capabilities"}, seq)
}

func upsertRuleEntry(doc *yaml.Node, in AgentSynthInput) {
	rules := ensureSequence(doc, "rules")
	for _, rule := range rules.Content {
		if rule.Kind != yaml.MappingNode {
			continue
		}
		_, match := findField(rule, "match")
		if match == nil || match.Kind != yaml.MappingNode {
			continue
		}
		_, tier := findField(match, "tier")
		if tier == nil || tier.Value != fmt.Sprintf("%d", in.Tier) {
			continue
		}
		appendBackendToRule(rule, in)
		return
	}
	// Create a new auto-rule for this tier.
	rule := buildAutoRule(in)
	rules.Content = append(rules.Content, rule)
}

func appendBackendToRule(rule *yaml.Node, in AgentSynthInput) {
	backends := ensureSequence(rule, "backends")
	for _, b := range backends.Content {
		if b.Value == in.Name {
			return // already present
		}
	}
	backends.Content = append(backends.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: in.Name})
	sortChainByCostClass(backends)
}

func buildAutoRule(in AgentSynthInput) *yaml.Node {
	rule := &yaml.Node{Kind: yaml.MappingNode}
	rule.Content = append(rule.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "name"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("tier-%d-auto", in.Tier)},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "priority"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", 100-in.Tier), Tag: "!!int"},
	)
	match := &yaml.Node{Kind: yaml.MappingNode}
	match.Content = append(match.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "tier"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", in.Tier), Tag: "!!int"},
	)
	rule.Content = append(rule.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "match"}, match,
	)
	backends := &yaml.Node{Kind: yaml.SequenceNode}
	backends.Content = append(backends.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: in.Name})
	rule.Content = append(rule.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "backends"}, backends,
	)
	return rule
}

// sortChainByCostClass keeps the backend chain in cheap → standard → premium
// order (cheapest first). Stable across calls so the YAML diff is minimal.
//
// Currently we have no in-tree cost_class for already-named scalar children
// of the rule's `backends:`, so this is a no-op stub kept for the design
// promise. Left in place for future cost-aware ordering once `backends:`
// becomes a richer structure.
func sortChainByCostClass(backends *yaml.Node) {
	// Sort alphabetically as a stable proxy. Not cost-aware until backends
	// list becomes a sequence of mappings instead of plain names.
	sort.SliceStable(backends.Content, func(i, j int) bool {
		return backends.Content[i].Value < backends.Content[j].Value
	})
}

func removeBackendStanza(doc *yaml.Node, name string) {
	_, backends := findField(doc, "backends")
	if backends == nil || backends.Kind != yaml.SequenceNode {
		return
	}
	out := backends.Content[:0]
	for _, item := range backends.Content {
		_, n := findField(item, "name")
		if n != nil && n.Value == name {
			continue
		}
		out = append(out, item)
	}
	backends.Content = out
}

func removeFromAllRules(doc *yaml.Node, name string) {
	_, rules := findField(doc, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return
	}
	for _, rule := range rules.Content {
		_, bs := findField(rule, "backends")
		if bs == nil || bs.Kind != yaml.SequenceNode {
			continue
		}
		out := bs.Content[:0]
		for _, item := range bs.Content {
			if item.Value != name {
				out = append(out, item)
			}
		}
		bs.Content = out
	}
}

// AgentConfigPath returns the canonical path to the router's config.yaml,
// honouring CONFIG_PATH env var or falling back to ./config.yaml.
func AgentConfigPath() string {
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		return v
	}
	if abs, err := filepath.Abs("config.yaml"); err == nil {
		return abs
	}
	return "config.yaml"
}
