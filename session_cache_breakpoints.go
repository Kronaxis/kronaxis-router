package main

import (
	"encoding/json"
)

// InjectAnthropicCacheBreakpoints transforms a chat-completions request
// body so that stable prefix entries carry Anthropic's
//
//	"cache_control": {"type": "ephemeral"}
//
// marker. The provider then caches the prefix's KV for ~5 minutes (or
// 1 hour with the persistent variant), and subsequent requests with the
// same prefix get a substantial latency + cost discount.
//
// We mark up to four boundaries (Anthropic's per request limit):
//  1. Last system message
//  2. Last assistant message before the new user turn
//
// These two cover the vast majority of agentic workloads where the
// system prompt and the most recent assistant turn are the heaviest
// stable bits. We deliberately do not mark every message because
// Anthropic counts each marked block toward the request's complexity
// and four is the documented hard limit.
//
// String-content entries are converted to the Anthropic content-array
// shape so cache_control can attach. Entries that are already arrays
// are scanned for the last text part and that part gets the marker.
//
// The function modifies body and returns a new []byte. If anything
// goes wrong (parse failure, no candidate entries to mark) the
// original body is returned unchanged.
func InjectAnthropicCacheBreakpoints(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	rawMsgs, ok := req["messages"].([]interface{})
	if !ok || len(rawMsgs) == 0 {
		return body
	}

	// Find the indices of: last system message, last assistant message.
	lastSystem := -1
	lastAssistant := -1
	for i, raw := range rawMsgs {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "system":
			lastSystem = i
		case "assistant":
			lastAssistant = i
		}
	}

	changed := false
	if lastSystem >= 0 {
		if markEntry(rawMsgs[lastSystem]) {
			changed = true
		}
	}
	if lastAssistant >= 0 && lastAssistant != lastSystem {
		if markEntry(rawMsgs[lastAssistant]) {
			changed = true
		}
	}
	if !changed {
		return body
	}

	req["messages"] = rawMsgs
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// markEntry attaches cache_control to one message entry. Returns true if
// the entry was successfully marked, false if the entry was malformed or
// already had a marker.
//
// Conversion rules:
//   - Content as string → wrap into [{type:"text", text:..., cache_control:...}]
//   - Content as []interface{} (already an array) → mark the last text part
//   - Anything else: skip
func markEntry(entry interface{}) bool {
	m, ok := entry.(map[string]interface{})
	if !ok {
		return false
	}
	switch content := m["content"].(type) {
	case string:
		m["content"] = []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          content,
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		}
		return true
	case []interface{}:
		// Find last text part and add cache_control if not already present.
		for i := len(content) - 1; i >= 0; i-- {
			part, ok := content[i].(map[string]interface{})
			if !ok {
				continue
			}
			if pType, _ := part["type"].(string); pType != "text" {
				continue
			}
			if _, exists := part["cache_control"]; exists {
				return false // already marked
			}
			part["cache_control"] = map[string]interface{}{"type": "ephemeral"}
			return true
		}
	}
	return false
}
