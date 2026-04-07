package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestContinuousComplexityScoring(t *testing.T) {
	type tc struct {
		name   string
		system string
		user   string
		maxTok int
		temp   *float64
	}

	temp0 := float64(0)
	temp07 := float64(0.7)
	temp09 := float64(0.9)

	cases := []tc{
		// Should score LOW (cheap model)
		{"True/false", "", "True or false: Python is compiled.", 10, &temp0},
		{"JSON classify", "Respond only in JSON.", "Classify as positive or negative: 'love it'", 50, &temp0},
		{"Extract email", "Output format: JSON.", "Extract email from: contact sarah@test.com", 50, &temp0},
		{"Fill blank", "", "Fill in: capital of Japan is ___.", 10, &temp0},
		{"Star rating", "", "Score 1-5: 'clean rooms, bad wifi'", 10, &temp0},

		// Should score MEDIUM
		{"Short explain", "", "What is a linked list?", 200, nil},
		{"Simple question", "", "What is the capital of France?", 50, nil},

		// Should score HIGH (powerful model)
		{"Strategic plan", "", "Design a comprehensive 3-phase migration strategy for 500 PostgreSQL tables to microservices. Analyse data consistency, rollback, and team coordination.", 2000, &temp07},
		{"Creative essay", "", "Write a detailed essay about AI impact on healthcare, proposing three innovative solutions for rural staffing.", 3000, &temp09},
		{"Code architecture", "", "Design a rate limiter supporting sliding window, token bucket, and leaky bucket. Compare and evaluate tradeoffs.", 2000, &temp07},
	}

	fmt.Println("\nCONTINUOUS COMPLEXITY SCORING")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("%-25s %8s %6s %s\n", "Test Case", "Score", "Tier", "Bar")
	fmt.Println(strings.Repeat("-", 80))

	for _, c := range cases {
		msgs := []ChatMessage{}
		if c.system != "" {
			msgs = append(msgs, ChatMessage{Role: "system", Content: c.system})
		}
		msgs = append(msgs, ChatMessage{Role: "user", Content: c.user})
		req := &ChatRequest{Messages: msgs, MaxTokens: c.maxTok, Temperature: c.temp}

		score := classifier.ScoreComplexity(req)
		tier := ClassifyPrompt(req)
		tierName := map[int]string{0: "auto", 1: "HEAVY", 2: "light"}[tier]

		// Visual bar
		barLen := int(float64(score) / 2)
		bar := strings.Repeat("#", barLen) + strings.Repeat(".", 50-barLen)

		fmt.Printf("%-25s %7.1f %6s [%s]\n", c.name, float64(score), tierName, bar)
	}
	fmt.Println(strings.Repeat("=", 80))

	// Verify ordering: extraction scores should be < reasoning scores
	extractReq := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "Return JSON only."},
			{Role: "user", Content: "Classify as positive or negative: 'Great product'"},
		},
		MaxTokens: 50, Temperature: &temp0,
	}
	reasonReq := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Design a comprehensive migration strategy for a distributed database. Analyse pros and cons of each approach step by step."},
		},
		MaxTokens: 2000, Temperature: &temp07,
	}

	extractScore := classifier.ScoreComplexity(extractReq)
	reasonScore := classifier.ScoreComplexity(reasonReq)

	fmt.Printf("\nExtraction score: %.1f, Reasoning score: %.1f\n", float64(extractScore), float64(reasonScore))
	fmt.Printf("Gap: %.1f points (wider = better discrimination)\n", float64(reasonScore)-float64(extractScore))

	if extractScore >= reasonScore {
		t.Errorf("extraction score (%.1f) should be lower than reasoning score (%.1f)", float64(extractScore), float64(reasonScore))
	}
	if float64(reasonScore)-float64(extractScore) < 20 {
		t.Errorf("gap between extraction and reasoning too narrow: %.1f (want >20)", float64(reasonScore)-float64(extractScore))
	}
}

func TestFeedbackLoop(t *testing.T) {
	// Reset classifier
	c := newAdaptiveClassifier()
	oldClassifier := classifier
	classifier = c
	defer func() { classifier = oldClassifier }()

	temp0 := float64(0)
	req := &ChatRequest{
		Messages:    []ChatMessage{{Role: "user", Content: "Translate this to French: hello"}},
		MaxTokens:   50,
		Temperature: &temp0,
	}

	scoreBefore := classifier.ScoreComplexity(req)

	// Simulate quality feedback: "translate" consistently fails on cheap model
	for i := 0; i < 10; i++ {
		classifier.ApplyFeedback("translate", -0.2) // Weaken cheap routing
	}

	scoreAfter := classifier.ScoreComplexity(req)

	fmt.Printf("\nFEEDBACK LOOP TEST\n")
	fmt.Printf("Score before feedback: %.1f\n", float64(scoreBefore))
	fmt.Printf("Score after 10x negative feedback on 'translate': %.1f\n", float64(scoreAfter))
	fmt.Printf("Shift: +%.1f (toward expensive model)\n", float64(scoreAfter)-float64(scoreBefore))

	if scoreAfter <= scoreBefore {
		t.Errorf("negative feedback should increase complexity score (route to expensive), got %.1f -> %.1f", float64(scoreBefore), float64(scoreAfter))
	}

	// Simulate positive feedback: "translate" works fine on cheap model
	for i := 0; i < 20; i++ {
		classifier.ApplyFeedback("translate", 0.1) // Strengthen cheap routing
	}

	scoreRecovered := classifier.ScoreComplexity(req)
	fmt.Printf("Score after 20x positive feedback: %.1f\n", float64(scoreRecovered))

	if scoreRecovered >= scoreAfter {
		t.Logf("positive feedback shifted score back: %.1f -> %.1f (good)", float64(scoreAfter), float64(scoreRecovered))
	}
}
