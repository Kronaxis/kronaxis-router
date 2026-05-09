package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// AnthropicSDKAdapter calls api.anthropic.com /v1/messages directly. This is
// the "cheap" leg: pure inference, no agentic loop, no tools, no file edits.
// Auth via ANTHROPIC_API_KEY env var (or AnthropicAPIKeyEnv config field).
//
// When called via the multi-account auth pool, AgentRequest.APIKey is already
// populated with the chosen account's key.
type AnthropicSDKAdapter struct {
	BaseURL    string
	APIKeyEnv  string
	HTTPClient *http.Client
}

func (a *AnthropicSDKAdapter) Name() string { return "anthropic-sdk" }

func (a *AnthropicSDKAdapter) Available() bool {
	if a.APIKeyEnv == "" {
		return false
	}
	return os.Getenv(a.APIKeyEnv) != ""
}

func (a *AnthropicSDKAdapter) Capabilities() Capabilities {
	return Capabilities{
		Tools:          false,
		FileEdit:       false,
		Streaming:      true,
		MaxContext:     200_000,
		SkillsCommands: false,
	}
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicSSEEvent struct {
	Type    string                 `json:"type"`
	Index   int                    `json:"index,omitempty"`
	Delta   *anthropicEventDelta   `json:"delta,omitempty"`
	Message *anthropicEventMessage `json:"message,omitempty"`
	Usage   *anthropicEventUsage   `json:"usage,omitempty"`
}

type anthropicEventDelta struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

type anthropicEventMessage struct {
	StopReason string               `json:"stop_reason,omitempty"`
	Usage      *anthropicEventUsage `json:"usage,omitempty"`
}

type anthropicEventUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (a *AnthropicSDKAdapter) Run(ctx context.Context, req AgentRequest, events chan<- AgentEvent) error {
	apiKey := req.APIKey
	if apiKey == "" {
		apiKey = os.Getenv(a.APIKeyEnv)
	}
	if apiKey == "" {
		return fmt.Errorf("no anthropic api key (env %s)", a.APIKeyEnv)
	}

	model := req.ClaudeModel
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Stream:    true,
	}
	if req.SystemPrompt != "" {
		body.System = req.SystemPrompt
	}
	if req.Temperature > 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			if body.System != "" {
				body.System += "\n\n"
			}
			body.System += m.Content
			continue
		}
		if role != "user" && role != "assistant" {
			role = "user"
		}
		body.Messages = append(body.Messages, anthropicMessage{
			Role:    role,
			Content: []anthropicContentBlock{{Type: "text", Text: m.Content}},
		})
	}
	if len(body.Messages) == 0 {
		return fmt.Errorf("no user/assistant messages to send")
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(a.BaseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Accept", "text/event-stream")

	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(buf))
	}

	return parseAnthropicSSE(resp.Body, events)
}

func parseAnthropicSSE(r io.Reader, events chan<- AgentEvent) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	stopReason := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var ev anthropicSSEEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta != nil && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				events <- AgentEvent{Type: EventTextDelta, Content: ev.Delta.Text}
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
		case "message_stop":
			events <- AgentEvent{Type: EventDone, StopReason: stopReason, NumTurns: 1}
			return nil
		case "error":
			events <- AgentEvent{Type: EventError, Error: payload}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan sse: %w", err)
	}
	events <- AgentEvent{Type: EventDone, StopReason: stopReason, NumTurns: 1}
	return nil
}

func newAnthropicSDKAdapter(cfg *Config) *AnthropicSDKAdapter {
	baseURL := cfg.AnthropicBaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	keyEnv := cfg.AnthropicAPIKeyEnv
	if keyEnv == "" {
		keyEnv = "ANTHROPIC_API_KEY"
	}
	return &AnthropicSDKAdapter{
		BaseURL:    baseURL,
		APIKeyEnv:  keyEnv,
		HTTPClient: &http.Client{Timeout: 0},
	}
}
