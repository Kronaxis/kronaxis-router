package main

import (
	"strings"
	"testing"
)

func TestCountTokens_Empty(t *testing.T) {
	if CountTokens("") != 0 {
		t.Error("empty string should be 0 tokens")
	}
}

func TestCountTokens_Short(t *testing.T) {
	tokens := CountTokens("Hi")
	if tokens != 1 {
		t.Errorf("short word should be ~1 token, got %d", tokens)
	}
}

func TestCountTokens_Sentence(t *testing.T) {
	tokens := CountTokens("The quick brown fox jumps over the lazy dog.")
	// ~10 words, short words, should be ~10-12 tokens
	if tokens < 8 || tokens > 15 {
		t.Errorf("expected 8-15 tokens for simple sentence, got %d", tokens)
	}
}

func TestCountTokens_LongText(t *testing.T) {
	// 100 words of medium length
	text := strings.Repeat("The artificial intelligence system processes natural language efficiently. ", 14)
	tokens := CountTokens(text)
	// ~100 words, avg 5-6 chars each, should be ~100-140 tokens
	if tokens < 80 || tokens > 250 {
		t.Errorf("expected 80-250 tokens for ~100 words, got %d", tokens)
	}
}

func TestCountTokens_Punctuation(t *testing.T) {
	tokens := CountTokens("Hello, world! How are you? I'm fine.")
	// 7 words + 4 punctuation marks
	if tokens < 8 || tokens > 15 {
		t.Errorf("expected 8-15 tokens with punctuation, got %d", tokens)
	}
}

func TestCountTokens_Numbers(t *testing.T) {
	tokens := CountTokens("The year is 2024 and the temperature is 23.5 degrees.")
	if tokens < 8 || tokens > 18 {
		t.Errorf("expected 8-18 tokens with numbers, got %d", tokens)
	}
}

func TestCountTokens_BetterThanDivFour(t *testing.T) {
	// Test that our estimate is closer to real tokenizer than len/4
	text := "Summarise the following document in three sentences."
	ourEstimate := CountTokens(text)
	naiveEstimate := len(text) / 4

	// Real cl100k_base: ~9 tokens
	// Our estimate should be closer to 9 than the naive one
	realTokens := 9
	ourDiff := abs(ourEstimate - realTokens)
	naiveDiff := abs(naiveEstimate - realTokens)

	if ourDiff > naiveDiff {
		t.Errorf("our estimate (%d) should be closer to real (%d) than naive (%d)", ourEstimate, realTokens, naiveEstimate)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
