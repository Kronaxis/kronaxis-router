package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInjectAnthropic_ConvertsStringSystemContent(t *testing.T) {
	body := []byte(`{
  "model": "claude-3-5-sonnet",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ]
}`)
	out := InjectAnthropicCacheBreakpoints(body)
	if !strings.Contains(string(out), "ephemeral") {
		t.Fatalf("output should contain 'ephemeral' marker:\n%s", string(out))
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	msgs := parsed["messages"].([]interface{})
	sys := msgs[0].(map[string]interface{})
	content, ok := sys["content"].([]interface{})
	if !ok {
		t.Fatalf("system content was not converted to array, got %T", sys["content"])
	}
	part := content[0].(map[string]interface{})
	if part["type"] != "text" {
		t.Errorf("part type = %v, want text", part["type"])
	}
	if _, has := part["cache_control"]; !has {
		t.Errorf("cache_control missing on system message")
	}
}

func TestInjectAnthropic_MarksLastAssistant(t *testing.T) {
	body := []byte(`{
  "messages": [
    {"role": "system", "content": "sys"},
    {"role": "user", "content": "u1"},
    {"role": "assistant", "content": "a1"},
    {"role": "user", "content": "u2 (new turn)"}
  ]
}`)
	out := InjectAnthropicCacheBreakpoints(body)
	var parsed map[string]interface{}
	_ = json.Unmarshal(out, &parsed)
	msgs := parsed["messages"].([]interface{})

	// Both system and last assistant should be marked.
	sys := msgs[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if _, ok := sys["cache_control"]; !ok {
		t.Error("system not marked")
	}
	asst := msgs[2].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if _, ok := asst["cache_control"]; !ok {
		t.Error("last assistant not marked")
	}

	// The user turns should NOT be marked (not in our two-anchor strategy).
	u1 := msgs[1].(map[string]interface{})
	if _, ok := u1["content"].(string); !ok {
		t.Error("u1 content should still be a string (not converted)")
	}
}

func TestInjectAnthropic_LeavesArrayContentAlone(t *testing.T) {
	body := []byte(`{
  "messages": [
    {"role": "system", "content": [
      {"type": "text", "text": "hello", "cache_control": {"type": "ephemeral"}}
    ]},
    {"role": "user", "content": "u1"}
  ]
}`)
	out := InjectAnthropicCacheBreakpoints(body)
	// Should be unchanged because the system already has cache_control.
	if string(out) != string(body) {
		// Allow whitespace differences from json.Marshal.
		var a, b map[string]interface{}
		_ = json.Unmarshal(body, &a)
		_ = json.Unmarshal(out, &b)
		// Re-marshal both with stable ordering for comparison.
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("array content with existing cache_control should be untouched\noriginal: %s\nout: %s", string(body), string(out))
		}
	}
}

func TestInjectAnthropic_BadJSONReturnsOriginal(t *testing.T) {
	body := []byte(`not json {{{`)
	out := InjectAnthropicCacheBreakpoints(body)
	if string(out) != string(body) {
		t.Errorf("malformed input should be returned unchanged")
	}
}

func TestInjectAnthropic_NoMessagesReturnsOriginal(t *testing.T) {
	body := []byte(`{"model": "claude-3-5-sonnet"}`)
	out := InjectAnthropicCacheBreakpoints(body)
	if string(out) != string(body) {
		t.Errorf("body without messages should be unchanged")
	}
}
