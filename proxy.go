package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Shared HTTP clients for connection reuse.
var (
	llmClient      = &http.Client{Timeout: 120 * time.Second}
	streamClient   = &http.Client{Timeout: 180 * time.Second}
)

// OpenAI-compatible request/response structures.

type ChatRequest struct {
	Model              string                 `json:"model"`
	Messages           []ChatMessage          `json:"messages"`
	MaxTokens          int                    `json:"max_tokens,omitempty"`
	Temperature        *float64               `json:"temperature,omitempty"`
	TopP               *float64               `json:"top_p,omitempty"`
	Stream             bool                   `json:"stream,omitempty"`
	Stop               interface{}            `json:"stop,omitempty"`
	FrequencyPenalty   *float64               `json:"frequency_penalty,omitempty"`
	PresencePenalty    *float64               `json:"presence_penalty,omitempty"`
	N                  int                    `json:"n,omitempty"`
	ChatTemplateKwargs map[string]interface{} `json:"chat_template_kwargs,omitempty"`
}

type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []ContentPart
}

type ContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *ImageURLDetail `json:"image_url,omitempty"`
}

type ImageURLDetail struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   *ChatUsage   `json:"usage,omitempty"`
}

type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// handleChatCompletions is the main proxy handler.
func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	// Read body
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrorJSON(w, 400, "invalid request body")
		return
	}

	// Track active requests
	incActive()
	defer decActive()

	// Parse request
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, 400, "invalid JSON: "+err.Error())
		return
	}

	// Extract routing metadata from headers
	meta := extractHeaders(r)
	meta.ModelField = req.Model
	meta.Stream = req.Stream
	meta.ContentType = detectContentType(req.Messages)

	// Score complexity and auto-classify tier if caller didn't set it
	meta.ComplexityScore = classifier.ScoreComplexity(&req)
	if meta.Tier == 0 {
		meta.Tier = ClassifyPrompt(&req)
	}

	// Check cache before doing anything expensive
	cacheHit, cacheable := "", false
	if cacheHit, cacheable = cacheKey(&req); cacheable {
		if body, statusCode, headers, ok := respCache.Get(cacheHit); ok {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("X-Kronaxis-Cache", "HIT")
			w.WriteHeader(statusCode)
			w.Write(body)
			return
		}
	}

	// Check budget FIRST -- reject before routing
	budgetResult := costs.checkBudget(meta.Service)
	if budgetResult.action == "reject" {
		writeErrorJSON(w, 429, fmt.Sprintf("daily budget exceeded for service %q", meta.Service))
		return
	}

	// Auto-batch: if priority is "bulk" and backend supports batch API,
	// automatically submit to async batch for 50% cost savings
	if meta.Priority == "bulk" {
		candidates := rtr.RouteCandidates(meta)
		if len(candidates) > 0 {
			provider := detectBatchProvider(candidates[0].Backend.Config.Type, candidates[0].Backend.Config.URL)
			if provider != "" {
				batchReqs := []BatchRequest{{
					CustomID: fmt.Sprintf("auto_%d", time.Now().UnixNano()),
					Body:     req,
				}}
				job, err := batchMgr.SubmitBatch(batchReqs, candidates[0].Backend.Config.Name)
				if err == nil {
					writeJSON(w, 202, map[string]interface{}{
						"batch":   true,
						"job_id":  job.ID,
						"message": "Request submitted to async batch API for 50% cost savings. Poll GET /api/batch?id=" + job.ID,
						"status":  job.Status,
					})
					return
				}
				// If batch submit fails, fall through to synchronous dispatch
				logger.Printf("auto-batch failed, falling back to sync: %v", err)
			}
		}
	}

	// Route: get all candidate backends in priority order
	candidates := rtr.RouteCandidates(meta)
	if len(candidates) == 0 {
		writeErrorJSON(w, 503, "no healthy backend available")
		return
	}

	// Budget downgrade: prepend cheaper backend if over budget
	if budgetResult.action == "downgrade" && budgetResult.downgradeTarget != "" {
		downgraded := pool.Get(budgetResult.downgradeTarget)
		if downgraded != nil && downgraded.IsAvailable() {
			candidates = append([]RouteResult{{
				Backend:   downgraded,
				ModelName: downgraded.Config.ModelName,
			}}, candidates...)
		}
	}

	start := time.Now()

	// Try each candidate with failover
	var lastErr error
	for i, candidate := range candidates {
		routeResult := candidate

		// Prepare request for this backend
		reqCopy := req
		injectQwenThinkingDisabled(&reqCopy, routeResult.Backend)
		reqCopy.Model = routeResult.ModelName
		modifiedBody, err := json.Marshal(reqCopy)
		if err != nil {
			continue
		}

		if i == 0 {
			addBrandingHeaders(w, routeResult)
		}

		// Streaming: no failover (headers already sent)
		if reqCopy.Stream {
			handleStreaming(w, r, routeResult, modifiedBody, &reqCopy, meta, start)
			return
		}

		// Throughput batching (background/bulk on first candidate only)
		if i == 0 && bat.ShouldBatch(meta) {
			entry := &BatchEntry{
				Body:   modifiedBody,
				Parsed: &reqCopy,
				Route:  routeResult,
				Meta:   meta,
			}
			respCh := bat.Enqueue(entry)
			batchResp := <-respCh

			latency := time.Since(start)
			if batchResp.Err != nil {
				logRequest(meta, routeResult, 0, 0, latency, false, batchResp.Err.Error())
				writeErrorJSON(w, 502, "backend error: "+batchResp.Err.Error())
				return
			}

			var chatResp ChatResponse
			json.Unmarshal(batchResp.Body, &chatResp)
			inputTokens, outputTokens := estimateTokens(&reqCopy, &chatResp)
			responseBody := postProcessResponse(batchResp.Body, routeResult.Backend)

			for k, v := range batchResp.Headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(batchResp.StatusCode)
			w.Write(responseBody)

			recordStat(meta, routeResult, latency, true)
			logRequest(meta, routeResult, inputTokens, outputTokens, latency, true, "")
			return
		}

		// Direct dispatch with retry
		statusCode, respHeaders, respBody, err := forwardWithRetry(
			routeResult.Backend, routeResult.ModelName, modifiedBody, &reqCopy, meta,
		)

		if err != nil {
			lastErr = err
			logger.Printf("backend %s failed, trying next (%d/%d): %v",
				routeResult.Backend.Config.Name, i+1, len(candidates), err)
			continue // Try next backend
		}

		// 5xx from backend: try next candidate
		if statusCode >= 500 {
			lastErr = fmt.Errorf("backend %s returned %d", routeResult.Backend.Config.Name, statusCode)
			logger.Printf("backend %s returned %d, trying next (%d/%d)",
				routeResult.Backend.Config.Name, statusCode, i+1, len(candidates))
			continue
		}

		// Success (or 4xx which is a client error, pass through)
		latency := time.Since(start)
		var chatResp ChatResponse
		json.Unmarshal(respBody, &chatResp)
		inputTokens, outputTokens := estimateTokens(&reqCopy, &chatResp)
		responseBody := postProcessResponse(respBody, routeResult.Backend)

		for k, v := range respHeaders {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		w.Write(responseBody)

		success := statusCode < 400
		if success && cacheable {
			respCache.Set(cacheHit, responseBody, statusCode, respHeaders)
		}
		// Quality validation: sample cheap-model responses
		if success && qualVal.ShouldSample() {
			content := ""
			if len(chatResp.Choices) > 0 {
				if s, ok := chatResp.Choices[0].Message.Content.(string); ok {
					content = s
				}
			}
			qualVal.ValidateAsync(meta, routeResult.Backend.Config.Name, content, modifiedBody, &reqCopy)
		}
		recordStat(meta, routeResult, latency, success)
		logRequest(meta, routeResult, inputTokens, outputTokens, latency, success, "")
		return
	}

	// All candidates failed
	latency := time.Since(start)
	errMsg := "all backends failed"
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	recordStat(meta, candidates[0], latency, false)
	logRequest(meta, candidates[0], 0, 0, latency, false, errMsg)
	writeErrorJSON(w, 502, "all backends failed: "+errMsg)
}

// forwardWithRetry wraps forwardToBackend with one retry on transient errors.
func forwardWithRetry(
	backend *Backend, modelName string, body []byte, req *ChatRequest, meta RouteRequest,
) (int, map[string]string, []byte, error) {
	statusCode, headers, respBody, err := forwardToBackend(backend, modelName, body, req, meta)
	if err == nil {
		return statusCode, headers, respBody, nil
	}

	// Retry once after a short backoff for transport errors
	time.Sleep(500 * time.Millisecond)
	return forwardToBackend(backend, modelName, body, req, meta)
}

// forwardToBackend sends a request to the selected backend and tracks errors
// for health scoring (including cloud backends that skip periodic health checks).
func forwardToBackend(
	backend *Backend,
	_ string,
	body []byte,
	req *ChatRequest,
	_ RouteRequest,
) (int, map[string]string, []byte, error) {
	backend.ActiveReqs.Add(1)
	defer backend.ActiveReqs.Add(-1)

	var statusCode int
	var headers map[string]string
	var respBody []byte
	var err error

	switch backend.Config.Type {
	case "gemini":
		statusCode, headers, respBody, err = forwardToGemini(backend, body, req)
	case "ollama":
		statusCode, headers, respBody, err = forwardToOllama(backend, body, req)
	default:
		statusCode, headers, respBody, err = forwardToOpenAI(backend, body)
	}

	// Track errors for health scoring (covers cloud backends too)
	backend.mu.Lock()
	if err != nil || statusCode >= 500 {
		backend.Failures++
		if backend.Failures >= 5 {
			if backend.Status != StatusDown {
				logger.Printf("backend %s marked DOWN (request errors: %d)", backend.Config.Name, backend.Failures)
			}
			backend.Status = StatusDown
		} else if backend.Failures >= 2 {
			backend.Status = StatusDegraded
		}
	} else {
		// Successful request: reset failure counter, mark healthy
		if backend.Status != StatusHealthy {
			logger.Printf("backend %s recovered via successful request", backend.Config.Name)
		}
		backend.Failures = 0
		backend.Status = StatusHealthy
	}
	backend.mu.Unlock()

	return statusCode, headers, respBody, err
}

// forwardToOpenAI sends to an OpenAI-compatible endpoint (vLLM, OpenAI, etc.)
func forwardToOpenAI(backend *Backend, body []byte) (int, map[string]string, []byte, error) {
	url := backend.Config.URL + "/v1/chat/completions"

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if backend.Config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+backend.Config.APIKey)
	}

	resp, err := llmClient.Do(httpReq)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}

	headers := map[string]string{
		"Content-Type": resp.Header.Get("Content-Type"),
	}
	return resp.StatusCode, headers, respBody, nil
}

// forwardToGemini transforms the request to Gemini's REST format.
func forwardToGemini(backend *Backend, _ []byte, req *ChatRequest) (int, map[string]string, []byte, error) {
	model := backend.Config.ModelName
	url := backend.Config.URL + "/models/" + model + ":generateContent"

	// Transform OpenAI format to Gemini format
	geminiReq := buildGeminiRequest(req)
	body, err := json.Marshal(geminiReq)
	if err != nil {
		return 0, nil, nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Use header-based auth to keep API key out of URLs and logs
	if backend.Config.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", backend.Config.APIKey)
	}

	resp, err := llmClient.Do(httpReq)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}

	if resp.StatusCode >= 500 {
		// Server error: treat as backend failure for failover
		return resp.StatusCode, nil, respBody, fmt.Errorf("gemini server error %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// Client error (429 rate limit, 403 auth, etc.): pass through to caller
		errResp := ChatResponse{
			ID: "chatcmpl-kronaxis", Object: "chat.completion", Created: time.Now().Unix(), Model: model,
			Choices: []ChatChoice{{Index: 0, Message: ChatMessage{Role: "assistant", Content: string(respBody)}, FinishReason: "error"}},
		}
		result, _ := json.Marshal(errResp)
		return resp.StatusCode, map[string]string{"Content-Type": "application/json"}, result, nil
	}

	// Transform Gemini response back to OpenAI format
	openAIResp := parseGeminiResponse(respBody, model)
	result, _ := json.Marshal(openAIResp)

	return 200, map[string]string{"Content-Type": "application/json"}, result, nil
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiContent           `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string          `json:"text,omitempty"`
	InlineData *geminiInline   `json:"inlineData,omitempty"`
}

type geminiInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
}

func buildGeminiRequest(req *ChatRequest) geminiRequest {
	gr := geminiRequest{}

	if req.MaxTokens > 0 || req.Temperature != nil || req.TopP != nil {
		gr.GenerationConfig = &geminiGenerationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
		}
	}

	var systemParts []geminiPart
	for _, msg := range req.Messages {
		content := messageToGeminiContent(msg)
		if msg.Role == "system" {
			// Concatenate multiple system messages
			systemParts = append(systemParts, content.Parts...)
		} else {
			role := "user"
			if msg.Role == "assistant" {
				role = "model"
			}
			content.Role = role
			gr.Contents = append(gr.Contents, content)
		}
	}
	if len(systemParts) > 0 {
		gr.SystemInstruction = &geminiContent{Parts: systemParts}
	}

	return gr
}

func messageToGeminiContent(msg ChatMessage) geminiContent {
	c := geminiContent{}

	switch v := msg.Content.(type) {
	case string:
		c.Parts = append(c.Parts, geminiPart{Text: v})
	case []interface{}:
		for _, part := range v {
			if m, ok := part.(map[string]interface{}); ok {
				partType, _ := m["type"].(string)
				switch partType {
				case "text":
					text, _ := m["text"].(string)
					c.Parts = append(c.Parts, geminiPart{Text: text})
				case "image_url":
					if imgURL, ok := m["image_url"].(map[string]interface{}); ok {
						urlStr, _ := imgURL["url"].(string)
						// Handle base64 data URIs
						if strings.HasPrefix(urlStr, "data:") {
							parts := strings.SplitN(urlStr, ",", 2)
							if len(parts) == 2 {
								mime := strings.TrimPrefix(strings.TrimSuffix(parts[0], ";base64"), "data:")
								c.Parts = append(c.Parts, geminiPart{
									InlineData: &geminiInline{MimeType: mime, Data: parts[1]},
								})
							}
						}
					}
				}
			}
		}
	}

	return c
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func parseGeminiResponse(body []byte, model string) ChatResponse {
	var gr geminiResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return ChatResponse{
			ID: "chatcmpl-kronaxis", Object: "chat.completion", Created: time.Now().Unix(), Model: model,
			Choices: []ChatChoice{{Index: 0, Message: ChatMessage{Role: "assistant", Content: "error parsing gemini response: " + err.Error()}, FinishReason: "error"}},
		}
	}

	resp := ChatResponse{
		ID:      "chatcmpl-kronaxis",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
	}

	if len(gr.Candidates) > 0 {
		text := ""
		for _, part := range gr.Candidates[0].Content.Parts {
			text += part.Text
		}
		resp.Choices = append(resp.Choices, ChatChoice{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: text},
			FinishReason: strings.ToLower(gr.Candidates[0].FinishReason),
		})
	}

	if gr.UsageMetadata != nil {
		resp.Usage = &ChatUsage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		}
	}

	return resp
}

// forwardToOllama transforms the request to Ollama's /api/chat format.
func forwardToOllama(backend *Backend, _ []byte, req *ChatRequest) (int, map[string]string, []byte, error) {
	url := backend.Config.URL + "/api/chat"

	type ollamaMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type ollamaReq struct {
		Model    string      `json:"model"`
		Messages []ollamaMsg `json:"messages"`
		Stream   bool        `json:"stream"`
	}

	or := ollamaReq{
		Model:  backend.Config.ModelName,
		Stream: false,
	}
	for _, msg := range req.Messages {
		text := ""
		if s, ok := msg.Content.(string); ok {
			text = s
		}
		or.Messages = append(or.Messages, ollamaMsg{Role: msg.Role, Content: text})
	}

	body, _ := json.Marshal(or)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := llmClient.Do(httpReq)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}

	if resp.StatusCode >= 400 {
		return resp.StatusCode, nil, respBody, fmt.Errorf("ollama error %d", resp.StatusCode)
	}

	// Transform Ollama response to OpenAI format
	var ollamaResp struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return 0, nil, nil, fmt.Errorf("failed to parse ollama response: %w", err)
	}

	openAIResp := ChatResponse{
		ID:      "chatcmpl-kronaxis",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   backend.Config.ModelName,
		Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: ollamaResp.Message.Content},
			FinishReason: "stop",
		}},
	}
	if ollamaResp.PromptEvalCount > 0 || ollamaResp.EvalCount > 0 {
		openAIResp.Usage = &ChatUsage{
			PromptTokens:     ollamaResp.PromptEvalCount,
			CompletionTokens: ollamaResp.EvalCount,
			TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		}
	}
	result, _ := json.Marshal(openAIResp)
	return 200, map[string]string{"Content-Type": "application/json"}, result, nil
}

// handleStreaming proxies SSE responses for real-time use cases.
func handleStreaming(
	w http.ResponseWriter,
	_ *http.Request,
	route RouteResult,
	body []byte,
	req *ChatRequest,
	meta RouteRequest,
	start time.Time,
) {
	backend := route.Backend

	// Only OpenAI-compatible backends support streaming via SSE
	if backend.Config.Type == "gemini" || backend.Config.Type == "ollama" {
		// Fall back to non-streaming (forwardToBackend manages its own ActiveReqs)
		statusCode, respHeaders, respBody, err := forwardToBackend(backend, route.ModelName, body, req, meta)
		latency := time.Since(start)
		if err != nil {
			recordStat(meta, route, latency, false)
			logRequest(meta, route, 0, 0, latency, false, err.Error())
			writeErrorJSON(w, 502, "backend error: "+err.Error())
			return
		}
		var chatResp ChatResponse
		json.Unmarshal(respBody, &chatResp)
		inputTokens, outputTokens := estimateTokens(req, &chatResp)
		responseBody := postProcessResponse(respBody, backend)
		for k, v := range respHeaders {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		w.Write(responseBody)
		success := statusCode < 400
		recordStat(meta, route, latency, success)
		logRequest(meta, route, inputTokens, outputTokens, latency, success, "")
		return
	}

	// Track active requests for the OpenAI streaming path
	backend.ActiveReqs.Add(1)
	defer backend.ActiveReqs.Add(-1)

	url := backend.Config.URL + "/v1/chat/completions"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		writeErrorJSON(w, 502, "failed to create request")
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if backend.Config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+backend.Config.APIKey)
	}

	resp, err := streamClient.Do(httpReq)
	if err != nil {
		writeErrorJSON(w, 502, "backend connection failed")
		return
	}
	defer resp.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrorJSON(w, 500, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	// Track accumulated content length for token estimation
	var totalOutputBytes int
	buf := make([]byte, 4096)
	inThinkBlock := false
	var thinkBuf strings.Builder

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			chunk, inThinkBlock, thinkBuf = stripThinkTagsStreaming(chunk, inThinkBlock, thinkBuf)
			if chunk != "" {
				totalOutputBytes += len(chunk)
				w.Write([]byte(chunk))
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}

	// Estimate tokens for cost tracking
	inputBytes := 0
	for _, msg := range req.Messages {
		if s, ok := msg.Content.(string); ok {
			inputBytes += len(s)
		}
	}
	inputTokens := inputBytes / 4
	outputTokens := totalOutputBytes / 4

	latency := time.Since(start)
	recordStat(meta, route, latency, true)
	logRequest(meta, route, inputTokens, outputTokens, latency, true, "")
}

// postProcessResponse handles think tag stripping and content branding.
func postProcessResponse(body []byte, _ *Backend) []byte {
	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return body // Not valid JSON, return as-is
	}

	modified := false

	for i := range resp.Choices {
		if content, ok := resp.Choices[i].Message.Content.(string); ok {
			// Strip think tags
			cleaned := stripThinkTags(content)

			// Inject content branding if enabled
			cleaned = injectContentBranding(cleaned)

			if cleaned != content {
				resp.Choices[i].Message.Content = cleaned
				modified = true
			}
		}
	}

	if !modified {
		return body
	}

	result, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return result
}

// injectContentBranding appends branding text if configured.
func injectContentBranding(content string) string {
	configMu.RLock()
	branding := cfg.Server.Branding
	configMu.RUnlock()

	if !branding.ContentInject {
		return content
	}

	// Skip injection for JSON responses
	if branding.ContentSkipJSON {
		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return content
		}
	}

	return content + branding.ContentText
}

// addBrandingHeaders adds Kronaxis branding headers to the response.
func addBrandingHeaders(w http.ResponseWriter, route RouteResult) {
	configMu.RLock()
	branding := cfg.Server.Branding
	configMu.RUnlock()

	if branding.Headers {
		w.Header().Set("X-Powered-By", branding.HeaderName)
		w.Header().Set("X-Kronaxis-Router-Version", version)
		if route.Backend != nil {
			w.Header().Set("X-Kronaxis-Backend", route.Backend.Config.Name)
		}
		if route.Rule != nil {
			w.Header().Set("X-Kronaxis-Rule", route.Rule.Name)
		}
		w.Header().Set("X-Kronaxis-Complexity", fmt.Sprintf("%.0f", float64(route.Complexity)))
	}
}

// detectContentType inspects messages for vision content (image_url parts).
func detectContentType(messages []ChatMessage) string {
	for _, msg := range messages {
		switch v := msg.Content.(type) {
		case []interface{}:
			for _, part := range v {
				if m, ok := part.(map[string]interface{}); ok {
					if t, _ := m["type"].(string); t == "image_url" {
						return "vision"
					}
				}
			}
		}
	}
	return "text"
}

// estimateTokens returns token counts from the response usage object,
// or estimates using BPE-approximation heuristics.
func estimateTokens(req *ChatRequest, resp *ChatResponse) (int, int) {
	if resp != nil && resp.Usage != nil && resp.Usage.TotalTokens > 0 {
		return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}

	// Estimate using BPE heuristic
	inputText := ""
	for _, msg := range req.Messages {
		if s, ok := msg.Content.(string); ok {
			inputText += s + " "
		}
	}

	outputText := ""
	if resp != nil {
		for _, choice := range resp.Choices {
			if s, ok := choice.Message.Content.(string); ok {
				outputText += s
			}
		}
	}

	return CountTokens(inputText), CountTokens(outputText)
}

func writeErrorJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    "router_error",
		},
	}
	data, _ := json.Marshal(resp)
	w.Write(data)
}

// jsonMarshal is a helper for consistent JSON encoding.
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// newJSONRequest creates a POST request with JSON content type and optional auth.
func newJSONRequest(url, apiKey string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, nil
}

// readBody reads and returns the full response body.
func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(resp.Body)
}
