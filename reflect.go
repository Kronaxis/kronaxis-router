package main

import (
	"encoding/json"
	"sync/atomic"
)

// System-2 reflection loops (ROADMAP #15). When a request opts in via
// X-Kronaxis-Reflect: 1, the router takes the model's first answer, asks the
// same backend to review it for errors/omissions, and returns the corrected
// answer. Opt-in only (it costs a second round-trip) and never on streaming.
// Best-effort: any failure returns the original answer unchanged.

const reflectionPrompt = "Review your previous answer for logical errors, incorrect claims, or omissions. " +
	"If it is already correct, return it unchanged. Reply with ONLY the corrected, final answer — no preamble, no explanation of changes."

var reflectionsTotal atomic.Uint64

// runReflection issues one review pass on the same backend and returns the
// refined response body. Returns (refinedBody, true) on success, or
// (initialBody, false) if anything went wrong (so the caller keeps the original).
func runReflection(req *ChatRequest, meta RouteRequest, backend *Backend, modelName string, initialBody []byte) ([]byte, bool) {
	var resp ChatResponse
	if err := json.Unmarshal(initialBody, &resp); err != nil || len(resp.Choices) == 0 {
		return initialBody, false
	}
	answer, ok := resp.Choices[0].Message.Content.(string)
	if !ok || answer == "" {
		return initialBody, false
	}

	// Build the review turn: original context + the model's answer + the review ask.
	rr := *req
	rr.Messages = append(append([]ChatMessage{}, req.Messages...),
		ChatMessage{Role: "assistant", Content: answer},
		ChatMessage{Role: "user", Content: reflectionPrompt},
	)
	rr.Stream = false
	body, err := json.Marshal(rr)
	if err != nil {
		return initialBody, false
	}

	status, _, refined, err := forwardToBackend(backend, modelName, body, &rr, meta)
	if err != nil || status >= 400 || len(refined) == 0 {
		return initialBody, false
	}
	// Sanity: the refined body must be a parseable chat response with content.
	var rresp ChatResponse
	if err := json.Unmarshal(refined, &rresp); err != nil || len(rresp.Choices) == 0 {
		return initialBody, false
	}
	if s, ok := rresp.Choices[0].Message.Content.(string); !ok || s == "" {
		return initialBody, false
	}
	reflectionsTotal.Add(1)
	return refined, true
}
