package main

import (
	"encoding/json"
	"sync"
	"time"
)

// BatchEntry represents a single request waiting to be dispatched.
type BatchEntry struct {
	Body       []byte
	Parsed     *ChatRequest
	Route      RouteResult
	Meta       RouteRequest
	ResponseCh chan *BatchResponse
	EnqueuedAt time.Time
}

// BatchResponse carries the result back to the waiting handler.
type BatchResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
	Err        error
}

// Batcher manages request dispatch strategies.
//
// For "interactive" and "normal" priority: immediate dispatch (no delay).
// For "background" and "bulk" priority on vLLM backends that share a model:
// collect requests over a short window and dispatch them as a single
// multi-prompt /v1/completions call when the backend supports it,
// or as concurrent individual calls otherwise.
type Batcher struct {
	config BatchingConfig
	queues map[string]*batchQueue // keyed by backend name
	mu     sync.RWMutex
}

type batchQueue struct {
	entries []*BatchEntry
	timer   *time.Timer
	mu      sync.Mutex
}

func newBatcher(config BatchingConfig) *Batcher {
	return &Batcher{
		config: config,
		queues: make(map[string]*batchQueue),
	}
}

func (b *Batcher) updateConfig(config BatchingConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = config
}

// ShouldBatch returns true if this request should go through the batcher
// instead of being dispatched immediately.
func (b *Batcher) ShouldBatch(meta RouteRequest) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.config.Enabled {
		return false
	}
	if meta.Stream {
		return false
	}
	for _, bypass := range b.config.PriorityBypass {
		if meta.Priority == bypass {
			return false
		}
	}
	// Only batch background and bulk work -- normal priority dispatches immediately
	return meta.Priority == "background" || meta.Priority == "bulk"
}

// Enqueue adds a request to the batch queue for its target backend.
// Returns a channel that will receive the response when dispatched.
func (b *Batcher) Enqueue(entry *BatchEntry) <-chan *BatchResponse {
	backendName := entry.Route.Backend.Config.Name
	entry.ResponseCh = make(chan *BatchResponse, 1)
	entry.EnqueuedAt = time.Now()

	b.mu.Lock()
	q, ok := b.queues[backendName]
	if !ok {
		q = &batchQueue{}
		b.queues[backendName] = q
	}
	windowMS := b.config.WindowMS
	maxSize := b.config.MaxBatchSize
	b.mu.Unlock()

	q.mu.Lock()
	q.entries = append(q.entries, entry)

	// If batch is full, dispatch immediately
	if len(q.entries) >= maxSize {
		entries := q.entries
		q.entries = nil
		if q.timer != nil {
			q.timer.Stop()
			q.timer = nil
		}
		q.mu.Unlock()
		go dispatchBatch(entries)
		return entry.ResponseCh
	}

	// Start the collection window timer (first entry starts the clock)
	if q.timer == nil {
		q.timer = time.AfterFunc(time.Duration(windowMS)*time.Millisecond, func() {
			q.mu.Lock()
			entries := q.entries
			q.entries = nil
			q.timer = nil
			q.mu.Unlock()
			if len(entries) > 0 {
				go dispatchBatch(entries)
			}
		})
	}

	q.mu.Unlock()
	return entry.ResponseCh
}

// dispatchBatch attempts true batching for vLLM backends (multi-prompt
// /v1/completions), falling back to concurrent individual dispatch.
func dispatchBatch(entries []*BatchEntry) {
	if len(entries) == 0 {
		return
	}

	// Check if all entries target the same vLLM backend and use the same model
	backend := entries[0].Route.Backend
	if backend.Config.Type == "vllm" && len(entries) > 1 && allSameModel(entries) {
		// Try true batch via /v1/completions (vLLM supports multiple prompts)
		if trueVLLMBatch(entries) {
			return
		}
		// Fall through to concurrent dispatch if batch endpoint fails
	}

	// Concurrent individual dispatch
	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Add(1)
		go func(e *BatchEntry) {
			defer wg.Done()
			e.ResponseCh <- dispatchSingle(e)
		}(entry)
	}
	wg.Wait()
}

// trueVLLMBatch sends multiple prompts to vLLM's /v1/completions endpoint
// in a single HTTP call. Returns true if successful.
func trueVLLMBatch(entries []*BatchEntry) bool {
	backend := entries[0].Route.Backend

	// Build a multi-prompt completions request
	// Flatten each chat request's messages into a single prompt string
	prompts := make([]string, len(entries))
	maxTokens := 0
	var temperature *float64
	for i, e := range entries {
		prompts[i] = flattenMessages(e.Parsed.Messages)
		if e.Parsed.MaxTokens > maxTokens {
			maxTokens = e.Parsed.MaxTokens
		}
		if e.Parsed.Temperature != nil && temperature == nil {
			temperature = e.Parsed.Temperature
		}
	}

	batchReq := map[string]interface{}{
		"model":  entries[0].Route.ModelName,
		"prompt": prompts,
	}
	if maxTokens > 0 {
		batchReq["max_tokens"] = maxTokens
	}
	if temperature != nil {
		batchReq["temperature"] = *temperature
	}
	// Inject Qwen thinking disable
	if entries[0].Parsed.ChatTemplateKwargs != nil {
		batchReq["chat_template_kwargs"] = entries[0].Parsed.ChatTemplateKwargs
	}

	body, err := json.Marshal(batchReq)
	if err != nil {
		return false
	}

	// Send to /v1/completions (not /v1/chat/completions)
	url := backend.Config.URL + "/v1/completions"
	resp, err := postJSON(url, backend.Config.APIKey, body)
	if err != nil {
		return false
	}

	// Parse vLLM batch completions response
	var batchResp struct {
		Choices []struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
		} `json:"choices"`
		Usage *ChatUsage `json:"usage"`
	}
	if err := json.Unmarshal(resp, &batchResp); err != nil {
		return false
	}

	// vLLM returns one choice per prompt, indexed by prompt position
	if len(batchResp.Choices) != len(entries) {
		return false // Unexpected response shape, fall back
	}

	for i, choice := range batchResp.Choices {
		// Convert completions response to chat response format
		chatResp := ChatResponse{
			ID:      "chatcmpl-kronaxis-batch",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   entries[i].Route.ModelName,
			Choices: []ChatChoice{{
				Index:        0,
				Message:      ChatMessage{Role: "assistant", Content: choice.Text},
				FinishReason: "stop",
			}},
		}
		if batchResp.Usage != nil && len(entries) > 0 {
			// Approximate per-request usage
			chatResp.Usage = &ChatUsage{
				PromptTokens:     batchResp.Usage.PromptTokens / len(entries),
				CompletionTokens: batchResp.Usage.CompletionTokens / len(entries),
				TotalTokens:      batchResp.Usage.TotalTokens / len(entries),
			}
		}
		respBody, _ := json.Marshal(chatResp)
		entries[i].ResponseCh <- &BatchResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       respBody,
		}
	}
	return true
}

func allSameModel(entries []*BatchEntry) bool {
	model := entries[0].Route.ModelName
	for _, e := range entries[1:] {
		if e.Route.ModelName != model {
			return false
		}
	}
	return true
}

// flattenMessages converts chat messages to a single prompt string
// using the ChatML format that most vLLM models expect.
func flattenMessages(messages []ChatMessage) string {
	var prompt string
	for _, msg := range messages {
		text := ""
		if s, ok := msg.Content.(string); ok {
			text = s
		}
		prompt += "<|im_start|>" + msg.Role + "\n" + text + "<|im_end|>\n"
	}
	prompt += "<|im_start|>assistant\n"
	return prompt
}

// postJSON is a helper for sending JSON POST requests.
func postJSON(url, apiKey string, body []byte) ([]byte, error) {
	req, err := newJSONRequest(url, apiKey, body)
	if err != nil {
		return nil, err
	}
	resp, err := llmClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readBody(resp)
}

// dispatchSingle forwards a single request to its routed backend.
func dispatchSingle(entry *BatchEntry) *BatchResponse {
	statusCode, headers, body, err := forwardToBackend(
		entry.Route.Backend,
		entry.Route.ModelName,
		entry.Body,
		entry.Parsed,
		entry.Meta,
	)
	return &BatchResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
		Err:        err,
	}
}
