package main

import (
	"strings"
	"unicode/utf8"
)

// Classifier auto-assigns routing tier based on prompt analysis.
// This removes the need for callers to set X-Kronaxis-Tier manually.
//
// The classification is heuristic-based (no ML model needed):
//   Tier 1 (heavy reasoning): planning, strategy, creative writing, analysis, long synthesis
//   Tier 2 (structured extraction): classification, scoring, JSON extraction, short answers
//
// When the caller explicitly sets X-Kronaxis-Tier, that takes precedence.
// Auto-classification only activates when Tier is 0 (unset).

// ClassifyPrompt analyses the request and returns a suggested tier.
// Returns 0 if classification is inconclusive (use default routing).
func ClassifyPrompt(req *ChatRequest) int {
	// Extract all text content from messages
	var allText string
	var userText string
	var systemText string
	for _, msg := range req.Messages {
		if s, ok := msg.Content.(string); ok {
			allText += s + " "
			if msg.Role == "user" {
				userText += s + " "
			}
			if msg.Role == "system" {
				systemText += s + " "
			}
		}
	}

	lower := strings.ToLower(allText)
	userLower := strings.ToLower(userText)

	// Vision content always gets its own routing (handled by content_type detection)
	// so we don't classify here.

	// ── Signal-based scoring ────────────────────────────────────────
	// Positive score = Tier 1 (heavy), Negative score = Tier 2 (light)
	score := 0

	// Length signals
	tokenEstimate := utf8.RuneCountInString(allText) / 4
	if tokenEstimate > 2000 {
		score += 2 // Long context = complex task
	} else if tokenEstimate < 100 {
		score -= 2 // Short prompt = simple task
	}

	// Tier 1 signals: tasks requiring reasoning, creativity, planning
	tier1Keywords := []string{
		"plan", "strategy", "analyse", "analyze", "design", "architect",
		"write a", "draft a", "compose", "create a detailed",
		"explain why", "reason about", "think through", "step by step",
		"pros and cons", "compare and contrast", "evaluate",
		"brainstorm", "generate ideas", "propose",
		"debug", "refactor", "implement",
		"multi-step", "complex", "nuanced", "comprehensive",
		"essay", "article", "report", "proposal", "narrative",
		"what would happen if", "how would you",
		"decision_loop", "reflection", "daily_plan", "monthly_plan",
	}
	for _, kw := range tier1Keywords {
		if strings.Contains(lower, kw) {
			score++
		}
	}

	// Tier 2 signals: tasks requiring extraction, classification, short output
	tier2Keywords := []string{
		"classify", "categorise", "categorize", "label",
		"extract", "parse", "score", "rate on a scale",
		"yes or no", "true or false", "which one",
		"return json", "respond in json", "output json", "json format",
		"summarise in one", "summarize in one", "one sentence", "one word",
		"sentiment", "positive or negative",
		"translate", "convert",
		"fill in", "complete the", "answer:",
		"standard_panel", "conjoint_rating", "emotional_state",
		"issp_questionnaire", "probe_response", "derive_beliefs",
	}
	for _, kw := range tier2Keywords {
		if strings.Contains(lower, kw) {
			score--
		}
	}

	// System prompt signals
	if strings.Contains(strings.ToLower(systemText), "respond only in json") ||
		strings.Contains(strings.ToLower(systemText), "output format: json") ||
		strings.Contains(strings.ToLower(systemText), "return a json") {
		score -= 2 // JSON-only output = structured extraction
	}

	// User prompt asking for specific format = extraction
	if strings.Contains(userLower, "on a scale of") ||
		strings.Contains(userLower, "rate from") ||
		strings.Contains(userLower, "pick one:") ||
		strings.Contains(userLower, "choose from:") {
		score -= 2
	}

	// Max tokens signal
	if req.MaxTokens > 0 && req.MaxTokens <= 200 {
		score-- // Short expected output = simple task
	}
	if req.MaxTokens > 1000 {
		score++ // Long expected output = complex task
	}

	// Temperature signal
	if req.Temperature != nil {
		if *req.Temperature == 0 {
			score-- // Deterministic = extraction/classification
		}
		if *req.Temperature >= 0.8 {
			score++ // High creativity = reasoning/writing
		}
	}

	// ── Decision ────────────────────────────────────────────────────
	if score >= 2 {
		return 1 // Tier 1: heavy reasoning
	}
	if score <= -2 {
		return 2 // Tier 2: structured extraction
	}
	return 0 // Inconclusive: let rules/defaults handle it
}

// AutoClassifyConfig controls automatic classification behaviour.
type AutoClassifyConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}
