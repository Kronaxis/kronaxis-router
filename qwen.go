package main

import (
	"strings"
)

// injectQwenThinkingDisabled adds chat_template_kwargs to disable thinking mode
// on Qwen3/3.5 models. Without this, the model wraps its output in <think> tags
// and produces no usable content.
func injectQwenThinkingDisabled(req *ChatRequest, backend *Backend) {
	modelLower := strings.ToLower(backend.Config.ModelName)
	if !strings.Contains(modelLower, "qwen3") {
		return
	}
	if req.ChatTemplateKwargs == nil {
		req.ChatTemplateKwargs = make(map[string]interface{})
	}
	req.ChatTemplateKwargs["enable_thinking"] = false
}

// stripThinkTags removes <think>...</think> blocks from LLM output.
// Handles both closed tags and unclosed tags (strips from <think> to end of string).
func stripThinkTags(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start == -1 {
			return s
		}
		end := strings.Index(s[start:], "</think>")
		if end == -1 {
			// Unclosed think tag: strip from <think> to end
			return strings.TrimSpace(s[:start])
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
}

// stripThinkTagsStreaming handles think tag stripping in SSE streaming mode.
// Returns the cleaned chunk, the updated inThinkBlock state, and the buffer.
func stripThinkTagsStreaming(
	chunk string,
	inThinkBlock bool,
	thinkBuf strings.Builder,
) (string, bool, strings.Builder) {
	if inThinkBlock {
		// Look for closing tag in this chunk
		endIdx := strings.Index(chunk, "</think>")
		if endIdx == -1 {
			// Still inside think block, buffer everything
			return "", true, thinkBuf
		}
		// Found end of think block
		remaining := chunk[endIdx+len("</think>"):]
		thinkBuf.Reset()
		return remaining, false, thinkBuf
	}

	// Check for opening think tag
	startIdx := strings.Index(chunk, "<think>")
	if startIdx == -1 {
		return chunk, false, thinkBuf
	}

	// Found opening tag
	before := chunk[:startIdx]
	after := chunk[startIdx+len("<think>"):]

	// Check if closing tag is in the same chunk
	endIdx := strings.Index(after, "</think>")
	if endIdx == -1 {
		// Opening tag but no close -- enter think block mode
		return before, true, thinkBuf
	}

	// Both open and close in same chunk
	remaining := after[endIdx+len("</think>"):]
	return before + remaining, false, thinkBuf
}
