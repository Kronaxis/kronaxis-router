package main

import (
	"testing"
)

func TestClassifyPrompt_HeavyReasoning(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Please analyse this situation step by step and design a comprehensive strategy for migrating our database. Consider pros and cons of each approach."},
		},
		MaxTokens: 2000,
	}
	tier := ClassifyPrompt(req)
	if tier != 1 {
		t.Errorf("expected tier 1 (heavy reasoning), got %d", tier)
	}
}

func TestClassifyPrompt_Extraction(t *testing.T) {
	temp := float64(0)
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "Respond only in JSON format."},
			{Role: "user", Content: "Classify this text as positive or negative: 'I love this product'"},
		},
		MaxTokens:   100,
		Temperature: &temp,
	}
	tier := ClassifyPrompt(req)
	if tier != 2 {
		t.Errorf("expected tier 2 (extraction), got %d", tier)
	}
}

func TestClassifyPrompt_Ambiguous(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Tell me about cats."},
		},
	}
	tier := ClassifyPrompt(req)
	if tier != 0 && tier != 2 {
		t.Errorf("short ambiguous prompt should be tier 0 or 2, got %d", tier)
	}
}

func TestClassifyPrompt_JSONOutput(t *testing.T) {
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "Output format: JSON with keys 'sentiment' and 'score'."},
			{Role: "user", Content: "Rate this review on a scale of 1-10: 'Great product, fast shipping'"},
		},
		MaxTokens: 50,
	}
	tier := ClassifyPrompt(req)
	if tier != 2 {
		t.Errorf("JSON extraction should be tier 2, got %d", tier)
	}
}

func TestClassifyPrompt_CreativeWriting(t *testing.T) {
	temp := float64(0.9)
	req := &ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Write a detailed essay about the impact of artificial intelligence on modern healthcare, proposing three innovative solutions."},
		},
		MaxTokens:   3000,
		Temperature: &temp,
	}
	tier := ClassifyPrompt(req)
	if tier != 1 {
		t.Errorf("creative writing should be tier 1, got %d", tier)
	}
}
