package main

import (
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// QualityValidator samples responses from cheaper models and validates
// them against a reference (expensive) model. If the cheap model's quality
// drops below threshold, the validator recommends promoting that task type.
//
// This creates a closed-loop cost optimisation: route to the cheapest model
// that demonstrably delivers acceptable quality.

type QualityConfig struct {
	Enabled        bool    `yaml:"enabled" json:"enabled"`
	SampleRate     float64 `yaml:"sample_rate" json:"sample_rate"`         // 0.0-1.0, fraction of requests to validate
	ReferenceModel string  `yaml:"reference_model" json:"reference_model"` // backend name for quality checks
	Threshold      float64 `yaml:"threshold" json:"threshold"`             // 0.0-1.0, minimum acceptable similarity
}

type QualityValidator struct {
	config     QualityConfig
	scores     map[string]*qualityScore // keyed by "service:call_type:backend"
	promotions map[string]bool          // task types that should be promoted
	checked    atomic.Int64
	passed     atomic.Int64
	failed     atomic.Int64
	sem        chan struct{} // bounds concurrent validation goroutines
	mu         sync.RWMutex
}

type qualityScore struct {
	Samples    int     `json:"samples"`
	AvgScore   float64 `json:"avg_score"`
	TotalScore float64 `json:"-"`
	LastCheck  time.Time `json:"last_check"`
}

func newQualityValidator(config QualityConfig) *QualityValidator {
	return &QualityValidator{
		config:     config,
		scores:     make(map[string]*qualityScore),
		promotions: make(map[string]bool),
		sem:        make(chan struct{}, 10), // max 10 concurrent quality checks
	}
}

func (qv *QualityValidator) updateConfig(config QualityConfig) {
	qv.mu.Lock()
	defer qv.mu.Unlock()
	qv.config = config
}

// ShouldSample returns true if this request should be quality-checked.
func (qv *QualityValidator) ShouldSample() bool {
	if !qv.config.Enabled || qv.config.SampleRate <= 0 {
		return false
	}
	return rand.Float64() < qv.config.SampleRate
}

// IsPromoted returns true if a task type has been flagged for promotion
// to a more capable model due to quality issues.
func (qv *QualityValidator) IsPromoted(service, callType, backendName string) bool {
	qv.mu.RLock()
	defer qv.mu.RUnlock()
	key := service + ":" + callType + ":" + backendName
	return qv.promotions[key]
}

// ValidateAsync sends the same prompt to the reference model in the background
// and compares the responses. Updates quality scores and promotion flags.
func (qv *QualityValidator) ValidateAsync(
	meta RouteRequest,
	backendName string,
	cheapResponse string,
	originalBody []byte,
	req *ChatRequest,
) {
	if qv.config.ReferenceModel == "" {
		return
	}
	refBackend := pool.Get(qv.config.ReferenceModel)
	if refBackend == nil || !refBackend.IsAvailable() {
		return
	}

	// Bounded concurrency: skip if semaphore full
	select {
	case qv.sem <- struct{}{}:
	default:
		return // Too many concurrent validations, skip
	}

	go func() {
		defer func() { <-qv.sem }()
		// Send same request to reference model
		refReq := *req
		refReq.Model = refBackend.Config.ModelName
		injectQwenThinkingDisabled(&refReq, refBackend)
		body, err := json.Marshal(refReq)
		if err != nil {
			return
		}

		statusCode, _, respBody, err := forwardToBackend(refBackend, refBackend.Config.ModelName, body, &refReq, meta)
		if err != nil || statusCode >= 400 {
			return
		}

		var refResp ChatResponse
		if err := json.Unmarshal(respBody, &refResp); err != nil || len(refResp.Choices) == 0 {
			return
		}
		refContent := ""
		if s, ok := refResp.Choices[0].Message.Content.(string); ok {
			refContent = stripThinkTags(s)
		}

		// Compare responses
		similarity := computeSimilarity(cheapResponse, refContent)

		// Update scores
		key := meta.Service + ":" + meta.CallType + ":" + backendName
		qv.mu.Lock()
		qs, ok := qv.scores[key]
		if !ok {
			qs = &qualityScore{}
			qv.scores[key] = qs
		}
		qs.Samples++
		qs.TotalScore += similarity
		qs.AvgScore = qs.TotalScore / float64(qs.Samples)
		qs.LastCheck = time.Now()

		qv.checked.Add(1)
		if similarity >= qv.config.Threshold {
			qv.passed.Add(1)
		} else {
			qv.failed.Add(1)
		}

		// Promote if average quality below threshold after enough samples
		if qs.Samples >= 5 && qs.AvgScore < qv.config.Threshold {
			if !qv.promotions[key] {
				qv.promotions[key] = true
				logger.Printf("[quality] promoting %s (avg score %.2f < threshold %.2f after %d samples)",
					key, qs.AvgScore, qv.config.Threshold, qs.Samples)
			}
		}
		// Demote (un-promote) if quality recovers
		if qs.Samples >= 10 && qs.AvgScore >= qv.config.Threshold+0.05 {
			if qv.promotions[key] {
				delete(qv.promotions, key)
				logger.Printf("[quality] demoting %s (avg score %.2f recovered above threshold)", key, qs.AvgScore)
			}
		}
		qv.mu.Unlock()

		// ── Feedback loop: adjust classifier weights ───────────────
		// Extract keywords from the prompt and adjust their weights
		// based on whether the cheap model produced good output.
		qv.feedbackToClassifier(req, similarity)
	}()
}

// Stats returns quality validation statistics.
func (qv *QualityValidator) Stats() map[string]interface{} {
	qv.mu.RLock()
	defer qv.mu.RUnlock()

	scores := make(map[string]interface{})
	for k, v := range qv.scores {
		scores[k] = v
	}

	promotions := make([]string, 0)
	for k := range qv.promotions {
		promotions = append(promotions, k)
	}

	return map[string]interface{}{
		"enabled":    qv.config.Enabled,
		"checked":    qv.checked.Load(),
		"passed":     qv.passed.Load(),
		"failed":     qv.failed.Load(),
		"scores":     scores,
		"promotions": promotions,
	}
}

// feedbackToClassifier adjusts classifier keyword weights based on quality results.
// If the cheap model scored well, light keywords in this prompt get strengthened
// (more aggressive routing to cheap model next time). If it scored poorly,
// light keywords get weakened (routes to expensive model next time).
func (qv *QualityValidator) feedbackToClassifier(req *ChatRequest, similarity float64) {
	// Extract all text from the prompt
	var allText string
	for _, msg := range req.Messages {
		if s, ok := msg.Content.(string); ok {
			allText += s + " "
		}
	}
	lower := strings.ToLower(allText)

	// Determine adjustment direction and magnitude
	// Good quality (>= threshold): nudge keywords toward cheap routing (+0.1)
	// Poor quality (< threshold): nudge keywords toward expensive routing (-0.2)
	// Asymmetric: penalise quality failures more than rewarding successes
	var adjustment float64
	if similarity >= qv.config.Threshold {
		adjustment = 0.1 // Cheap model worked: slightly strengthen cheap routing
	} else {
		adjustment = -0.2 // Cheap model failed: more strongly weaken cheap routing
	}

	classifier.mu.RLock()
	// Check which light keywords appear in this prompt
	for _, kw := range classifier.lightKeywords {
		if strings.Contains(lower, kw.Keyword) {
			classifier.mu.RUnlock()
			classifier.ApplyFeedback(kw.Keyword, adjustment)
			classifier.mu.RLock()
		}
	}
	// Check which heavy keywords appear
	for _, kw := range classifier.heavyKeywords {
		if strings.Contains(lower, kw.Keyword) {
			classifier.mu.RUnlock()
			classifier.ApplyFeedback(kw.Keyword, -adjustment) // Inverse for heavy keywords
			classifier.mu.RLock()
		}
	}
	classifier.mu.RUnlock()
}

// computeSimilarity calculates a simple similarity score between two texts.
// Uses token overlap (Jaccard similarity on word sets) as a lightweight proxy
// for semantic similarity. Returns 0.0-1.0.
func computeSimilarity(a, b string) float64 {
	if a == "" && b == "" {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}

	wordsA := tokenise(a)
	wordsB := tokenise(b)

	setA := make(map[string]bool)
	for _, w := range wordsA {
		setA[w] = true
	}
	setB := make(map[string]bool)
	for _, w := range wordsB {
		setB[w] = true
	}

	intersection := 0
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 1.0
	}

	jaccard := float64(intersection) / float64(union)

	// Also compare length ratio (penalise very different lengths)
	lenRatio := float64(len(a)) / float64(len(b))
	if lenRatio > 1 {
		lenRatio = 1 / lenRatio
	}

	// Weighted: 70% token overlap, 30% length similarity
	return 0.7*jaccard + 0.3*lenRatio
}

func tokenise(s string) []string {
	s = strings.ToLower(s)
	var words []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, ".,!?;:\"'()[]{}/-")
		if len(w) > 1 {
			words = append(words, w)
		}
	}
	return words
}
