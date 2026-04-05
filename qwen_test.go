package main

import (
	"strings"
	"testing"
)

func TestStripThinkTags_Closed(t *testing.T) {
	input := "Hello <think>internal reasoning</think> world"
	expected := "Hello  world"
	result := stripThinkTags(input)
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestStripThinkTags_Unclosed(t *testing.T) {
	input := "Hello <think>reasoning goes on forever"
	expected := "Hello"
	result := stripThinkTags(input)
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestStripThinkTags_Multiple(t *testing.T) {
	input := "<think>first</think>A<think>second</think>B"
	expected := "AB"
	result := stripThinkTags(input)
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestStripThinkTags_NoTags(t *testing.T) {
	input := "Just normal text"
	result := stripThinkTags(input)
	if result != input {
		t.Errorf("should not modify text without tags, got %q", result)
	}
}

func TestStripThinkTags_Empty(t *testing.T) {
	result := stripThinkTags("")
	if result != "" {
		t.Errorf("empty string should return empty, got %q", result)
	}
}

func TestInjectQwenThinkingDisabled(t *testing.T) {
	req := &ChatRequest{Model: "test"}
	backend := &Backend{Config: BackendConfig{ModelName: "Qwen3.5-27B"}}

	injectQwenThinkingDisabled(req, backend)

	if req.ChatTemplateKwargs == nil {
		t.Fatal("ChatTemplateKwargs should be set")
	}
	if val, ok := req.ChatTemplateKwargs["enable_thinking"]; !ok || val != false {
		t.Error("enable_thinking should be false")
	}
}

func TestInjectQwenThinkingDisabled_NonQwen(t *testing.T) {
	req := &ChatRequest{Model: "test"}
	backend := &Backend{Config: BackendConfig{ModelName: "llama-3"}}

	injectQwenThinkingDisabled(req, backend)

	if req.ChatTemplateKwargs != nil {
		t.Error("should not inject for non-Qwen models")
	}
}

func TestStripThinkTagsStreaming(t *testing.T) {
	tests := []struct {
		name         string
		chunk        string
		inBlock      bool
		expectOut    string
		expectBlock  bool
	}{
		{"no tags", "hello world", false, "hello world", false},
		{"open tag starts block", "before<think>inside", false, "before", true},
		{"close tag ends block", "inside</think>after", true, "after", false},
		{"both in one chunk", "a<think>b</think>c", false, "ac", false},
		{"still in block", "more content", true, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			out, block, _ := stripThinkTagsStreaming(tt.chunk, tt.inBlock, buf)
			if out != tt.expectOut {
				t.Errorf("output: expected %q, got %q", tt.expectOut, out)
			}
			if block != tt.expectBlock {
				t.Errorf("inBlock: expected %v, got %v", tt.expectBlock, block)
			}
		})
	}
}
