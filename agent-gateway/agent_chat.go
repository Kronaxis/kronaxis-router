package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handleProfileChat services /v1/chat/completions for models that resolve to
// a profile in the new registry. It uses the runner / accounts / workspace
// packages directly and shares the same SSE / buffered output shape as the
// legacy path.
func (s *Server) handleProfileChat(w http.ResponseWriter, r *http.Request, req *ChatCompletionRequest, requestID string, startTime time.Time, rec *RequestRecord) {
	// Per-profile limits FIRST (per-agent concurrency + rate). If the rate
	// bucket is empty, return 429 immediately rather than blocking. Then
	// take the global semaphore to cap total in-flight gateway requests.
	profileName, _ := splitAgentSubmodel(req.Model)
	var profileRelease func() = func() {}
	if s.profileReg != nil && s.profileLimits != nil {
		if prof, ok := s.profileReg.Get(profileName); ok {
			pl := s.profileLimits.Get(profileName, prof.Limits.Concurrency, prof.Limits.RatePerMinute)
			rel, ok := pl.Acquire(r.Context().Done())
			if !ok {
				rel()
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded for agent "+profileName, http.StatusTooManyRequests)
				return
			}
			profileRelease = rel
		}
	}
	defer profileRelease()

	// Concurrency cap (gateway-wide).
	select {
	case s.sem <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	defer func() { <-s.sem }()
	s.metrics.RequestStarted()

	// Apply skill prefix to first user message.
	if skill := strings.TrimSpace(req.Skill); skill != "" {
		req.Messages = applySkillPrefix(req.Messages, skill)
	}

	// Per-profile timeout override falls back to gateway default.
	timeoutSec := s.cfg.TimeoutSeconds
	if s.profileReg != nil {
		if prof, ok := s.profileReg.Get(profileName); ok && prof.Limits.TimeoutSeconds > 0 {
			timeoutSec = prof.Limits.TimeoutSeconds
		}
	}

	agentReq := AgentRequest{
		Messages:           req.Messages,
		Model:              req.Model,
		RequestID:          requestID,
		TimeoutSec:         timeoutSec,
		SystemPrompt:       req.SystemPrompt,
		AppendSystemPrompt: req.AppendSystemPrompt,
		Agent:              req.Agent,
		PermissionMode:     req.PermissionMode,
		Effort:             req.Effort,
		AllowedTools:       req.AllowedTools,
		DisallowedTools:    req.DisallowedTools,
		MCPConfig:          req.MCPConfig,
		AddDirs:            req.AddDirs,
		Bare:               req.Bare,
		IncludeHookEvents:  req.IncludeHookEvents,
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	events := make(chan AgentEvent, 64)
	runErrCh := make(chan error, 1)
	go func() {
		_, err := s.dispatchProfileAgent(ctx, req.Model, agentReq, events)
		runErrCh <- err
		close(events)
	}()

	rec.Adapter = "profile:" + req.Model

	if req.Stream {
		s.streamProfileChat(ctx, w, req.Model, events, runErrCh, requestID, startTime, rec)
		return
	}
	s.bufferedProfileChat(ctx, w, req.Model, events, runErrCh, requestID, startTime, rec)
}

func (s *Server) streamProfileChat(ctx context.Context, w http.ResponseWriter, model string, events <-chan AgentEvent, runErrCh <-chan error, requestID string, startTime time.Time, rec *RequestRecord) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	created := time.Now().Unix()
	chatID := "chatcmpl-" + requestID

	// Stream "first" delta with role=assistant per OpenAI convention.
	first := openAIChunk(chatID, model, created, &openAIDelta{Role: "assistant"}, "")
	_, _ = w.Write(formatSSE(first))
	flusher.Flush()

	keepalive := time.Duration(s.cfg.KeepaliveSeconds) * time.Second
	if keepalive <= 0 {
		keepalive = 15 * time.Second
	}
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()

	var totalText strings.Builder
	finalStop := "stop"
	var totalIn, totalOut int

L:
	for {
		select {
		case <-ctx.Done():
			break L
		case <-ticker.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				break L
			}
			switch ev.Type {
			case EventTextDelta:
				if ev.Content != "" {
					totalText.WriteString(ev.Content)
					chunk := openAIChunk(chatID, model, created, &openAIDelta{Content: ev.Content}, "")
					_, _ = w.Write(formatSSE(chunk))
					flusher.Flush()
				}
			case EventToolCallStart:
				if ev.Tool != nil {
					call := openAIToolCall{Index: ev.Tool.Index, ID: ev.Tool.ID, Type: "function"}
					call.Function.Name = ev.Tool.Name
					chunk := openAIChunk(chatID, model, created, &openAIDelta{ToolCalls: []openAIToolCall{call}}, "")
					_, _ = w.Write(formatSSE(chunk))
					flusher.Flush()
				}
			case EventToolCallDelta:
				if ev.Tool != nil && ev.Tool.PartialJSON != "" {
					call := openAIToolCall{Index: ev.Tool.Index}
					call.Function.Arguments = ev.Tool.PartialJSON
					chunk := openAIChunk(chatID, model, created, &openAIDelta{ToolCalls: []openAIToolCall{call}}, "")
					_, _ = w.Write(formatSSE(chunk))
					flusher.Flush()
				}
			case EventDone:
				if ev.StopReason != "" {
					finalStop = ev.StopReason
				}
				totalIn = ev.InputTokens
				totalOut = ev.OutputTokens
			case EventError:
				_, _ = w.Write([]byte("data: " + sseError(ev.Error) + "\n\n"))
				flusher.Flush()
			}
		}
	}

	stopChunk := openAIChunk(chatID, model, created, nil, finalStop)
	_, _ = w.Write(formatSSE(stopChunk))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()

	if err := <-runErrCh; err != nil {
		s.logger.Printf("[%s] runner err: %v", requestID, err)
		rec.Error = err.Error()
		rec.Status = "error"
	} else {
		rec.Status = "ok"
	}
	rec.HTTPStatus = http.StatusOK
	rec.DurationMS = time.Since(startTime).Milliseconds()
	rec.InputTokens = totalIn
	rec.OutputTokens = totalOut
	
	s.metrics.RequestFinished(rec.Adapter, model, rec.Status, rec.DurationMS, 0, 1)
	s.audit.Request(*rec)
}

func (s *Server) bufferedProfileChat(ctx context.Context, w http.ResponseWriter, model string, events <-chan AgentEvent, runErrCh <-chan error, requestID string, startTime time.Time, rec *RequestRecord) {
	var content strings.Builder
	finalStop := "stop"
	var totalIn, totalOut int
	var firstErr string
	tools := []openAIToolCall{}
	toolByIdx := map[int]*openAIToolCall{}

L:
	for {
		select {
		case <-ctx.Done():
			break L
		case ev, ok := <-events:
			if !ok {
				break L
			}
			switch ev.Type {
			case EventTextDelta:
				content.WriteString(ev.Content)
			case EventToolCallStart:
				if ev.Tool != nil {
					t := openAIToolCall{Index: ev.Tool.Index, ID: ev.Tool.ID, Type: "function"}
					t.Function.Name = ev.Tool.Name
					tools = append(tools, t)
					toolByIdx[ev.Tool.Index] = &tools[len(tools)-1]
				}
			case EventToolCallDelta:
				if ev.Tool != nil {
					if t, ok := toolByIdx[ev.Tool.Index]; ok {
						t.Function.Arguments += ev.Tool.PartialJSON
					}
				}
			case EventDone:
				if ev.StopReason != "" {
					finalStop = ev.StopReason
				}
				totalIn = ev.InputTokens
				totalOut = ev.OutputTokens
			case EventError:
				if firstErr == "" {
					firstErr = ev.Error
				}
			}
		}
	}
	if err := <-runErrCh; err != nil && firstErr == "" {
		firstErr = err.Error()
	}
	resp := map[string]any{
		"id":      "chatcmpl-" + requestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content.String(),
					"tool_calls": func() any {
						if len(tools) == 0 {
							return nil
						}
						return tools
					}(),
				},
				"finish_reason": finalStop,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     totalIn,
			"completion_tokens": totalOut,
			"total_tokens":      totalIn + totalOut,
		},
	}
	if firstErr != "" {
		resp["error"] = firstErr
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)

	if firstErr != "" {
		rec.Status = "error"
		rec.Error = firstErr
	} else {
		rec.Status = "ok"
	}
	rec.HTTPStatus = http.StatusOK
	rec.DurationMS = time.Since(startTime).Milliseconds()
	rec.InputTokens = totalIn
	rec.OutputTokens = totalOut
	
	s.metrics.RequestFinished(rec.Adapter, model, rec.Status, rec.DurationMS, 0, 1)
	s.audit.Request(*rec)
}

// formatSSE wraps a JSON object as a single SSE data: line.
func formatSSE(v any) []byte {
	b, _ := json.Marshal(v)
	return []byte("data: " + string(b) + "\n\n")
}

// sseError formats a single error payload as JSON for SSE emission.
func sseError(msg string) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "agent_error"}})
	return string(b)
}

// openAIChunk constructs a chat.completion.chunk object.
func openAIChunk(id, model string, created int64, delta *openAIDelta, finishReason string) map[string]any {
	choice := map[string]any{"index": 0}
	if delta != nil {
		choice["delta"] = delta
	} else {
		choice["delta"] = struct{}{}
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{choice},
	}
}

type openAIDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// (compile-time guard against unused imports if helpers above are inlined elsewhere)
var _ = fmt.Sprintf
