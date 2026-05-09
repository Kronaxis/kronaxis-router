package main

import (
	"math"
	"strings"
	"sync"
	"unicode/utf8"
)

// Classifier auto-assigns routing based on prompt complexity analysis.
//
// Instead of binary tier assignment (1=heavy, 2=light), the classifier
// outputs a continuous complexity score (0-100) that maps to backends
// via configurable thresholds. This allows fine-grained model selection
// across any number of model sizes.
//
// The feedback loop reads quality validation results and adjusts keyword
// weights over time. Keywords that consistently route to cheap models
// with good quality get stronger negative weights. Keywords that cause
// quality problems get weaker weights or flip to positive.
//
// When the caller explicitly sets X-Kronaxis-Tier, that takes precedence.

// ComplexityScore is 0-100 where 0 is trivial and 100 is highly complex.
type ComplexityScore float64

// Default thresholds for backward-compatible tier mapping.
const (
	Tier2Ceiling = 35.0  // score <= 35 -> tier 2 (cheap)
	Tier1Floor   = 65.0  // score >= 65 -> tier 1 (heavy)
	// 36-64 = inconclusive (routes to default/fallback)
)

// keywordWeight holds a keyword and its current weight, adjustable by feedback.
type keywordWeight struct {
	Keyword string
	Weight  float64
}

// AdaptiveClassifier scores prompt complexity with feedback-adjusted weights.
type AdaptiveClassifier struct {
	heavyKeywords []keywordWeight
	lightKeywords []keywordWeight
	feedback      map[string]float64 // keyword -> adjustment from quality feedback
	mu            sync.RWMutex
}

func newAdaptiveClassifier() *AdaptiveClassifier {
	return &AdaptiveClassifier{
		heavyKeywords: []keywordWeight{
			{"plan", 4}, {"strategy", 4}, {"analyse", 5}, {"analyze", 5},
			{"design", 4}, {"architect", 5},
			{"write a", 3}, {"draft a", 3}, {"compose", 3}, {"create a detailed", 5},
			{"explain why", 4}, {"reason about", 5}, {"think through", 5}, {"step by step", 5},
			{"pros and cons", 5}, {"compare and contrast", 5}, {"evaluate", 4},
			{"brainstorm", 4}, {"generate ideas", 4}, {"propose", 3},
			{"debug", 3}, {"refactor", 4}, {"implement", 3},
			{"multi-step", 5}, {"complex", 3}, {"nuanced", 4}, {"comprehensive", 4},
			{"essay", 5}, {"article", 4}, {"report", 4}, {"proposal", 5}, {"narrative", 4},
			{"what would happen if", 5}, {"how would you", 3},
			{"decision_loop", 5}, {"reflection", 4}, {"daily_plan", 4}, {"monthly_plan", 5},
		},
		lightKeywords: []keywordWeight{
			{"classify", 5}, {"categorise", 5}, {"categorize", 5}, {"label", 4},
			{"extract", 5}, {"parse", 5}, {"score", 3}, {"rate on a scale", 5},
			{"yes or no", 6}, {"true or false", 6}, {"which one", 4},
			{"return json", 6}, {"respond in json", 6}, {"output json", 6}, {"json format", 5},
			{"summarise in one", 5}, {"summarize in one", 5}, {"one sentence", 4}, {"one word", 5},
			{"sentiment", 5}, {"positive or negative", 5},
			{"translate", 4}, {"convert", 3},
			{"fill in", 4}, {"complete the", 3}, {"answer:", 3},
			{"form_field", 4}, {"lookup_value", 4}, {"match_record", 4},
			{"validate_input", 4}, {"check_status", 4}, {"format_output", 4},
		},
		feedback: make(map[string]float64),
	}
}

// ScoreComplexity returns a 0-100 complexity score for the request.
// 0 = trivial (cheapest model), 100 = highly complex (most capable model).
func (ac *AdaptiveClassifier) ScoreComplexity(req *ChatRequest) ComplexityScore {
	var allText, userText, systemText string
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
	systemLower := strings.ToLower(systemText)

	// Raw score: positive = complex, negative = simple
	rawScore := 0.0

	// ── Length signals ──────────────────────────────────────────────
	tokenEstimate := utf8.RuneCountInString(allText) / 4
	switch {
	case tokenEstimate > 3000:
		rawScore += 15
	case tokenEstimate > 1500:
		rawScore += 8
	case tokenEstimate > 500:
		rawScore += 3
	case tokenEstimate < 50:
		rawScore -= 10
	case tokenEstimate < 150:
		rawScore -= 5
	}

	// ── Keyword signals (with feedback adjustments) ────────────────
	ac.mu.RLock()
	for _, kw := range ac.heavyKeywords {
		if strings.Contains(lower, kw.Keyword) {
			weight := kw.Weight + ac.feedback[kw.Keyword]
			rawScore += weight
		}
	}
	for _, kw := range ac.lightKeywords {
		if strings.Contains(lower, kw.Keyword) {
			weight := kw.Weight + ac.feedback[kw.Keyword]
			rawScore -= weight
		}
	}
	ac.mu.RUnlock()

	// ── System prompt signals ──────────────────────────────────────
	if strings.Contains(systemLower, "respond only in json") ||
		strings.Contains(systemLower, "output format: json") ||
		strings.Contains(systemLower, "return a json") {
		rawScore -= 10
	}

	// ── User format signals ────────────────────────────────────────
	if strings.Contains(userLower, "on a scale of") ||
		strings.Contains(userLower, "rate from") ||
		strings.Contains(userLower, "pick one:") ||
		strings.Contains(userLower, "choose from:") {
		rawScore -= 8
	}

	// ── Max tokens signal ──────────────────────────────────────────
	if req.MaxTokens > 0 {
		switch {
		case req.MaxTokens <= 50:
			rawScore -= 8
		case req.MaxTokens <= 200:
			rawScore -= 4
		case req.MaxTokens >= 2000:
			rawScore += 8
		case req.MaxTokens >= 1000:
			rawScore += 4
		}
	}

	// ── Temperature signal ─────────────────────────────────────────
	if req.Temperature != nil {
		if *req.Temperature == 0 {
			rawScore -= 5 // Deterministic = extraction
		}
		if *req.Temperature >= 0.8 {
			rawScore += 5 // Creative
		}
	}

	// ── Conversation depth ─────────────────────────────────────────
	msgCount := len(req.Messages)
	if msgCount > 6 {
		rawScore += 5 // Multi-turn = complex context
	}

	// ── Map raw score to 0-100 ─────────────────────────────────────
	// Raw range is roughly -40 to +40. Map to 0-100 using sigmoid.
	normalized := sigmoid(rawScore / 15) * 100

	return ComplexityScore(normalized)
}

// ClassifyPrompt returns a backward-compatible tier (0, 1, or 2) from the complexity score.
func ClassifyPrompt(req *ChatRequest) int {
	score := classifier.ScoreComplexity(req)
	if float64(score) >= Tier1Floor {
		return 1
	}
	if float64(score) <= Tier2Ceiling {
		return 2
	}
	return 0
}

// ApplyFeedback adjusts keyword weights based on quality validation results.
// Called by the quality validator when it has enough samples for a keyword pattern.
//
// If a keyword consistently appears in prompts where the cheap model performs well,
// its "light" weight increases (routes more aggressively to cheap model).
// If a keyword appears in prompts where the cheap model fails, its "light" weight
// decreases (routes to expensive model instead).
func (ac *AdaptiveClassifier) ApplyFeedback(keyword string, adjustment float64) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	current := ac.feedback[keyword]
	// Clamp adjustments to prevent runaway drift
	newVal := current + adjustment
	if newVal > 3.0 {
		newVal = 3.0
	}
	if newVal < -3.0 {
		newVal = -3.0
	}
	ac.feedback[keyword] = newVal
}

// FeedbackState returns the current keyword adjustments for inspection.
func (ac *AdaptiveClassifier) FeedbackState() map[string]float64 {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	state := make(map[string]float64, len(ac.feedback))
	for k, v := range ac.feedback {
		state[k] = v
	}
	return state
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// Global classifier instance
var classifier = newAdaptiveClassifier()
