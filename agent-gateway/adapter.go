package main

import (
	"context"
	"errors"
)

type EventType string

const (
	EventTextDelta     EventType = "text_delta"
	EventToolCallStart EventType = "tool_call_start"
	EventToolCallDelta EventType = "tool_call_delta"
	EventToolCallEnd   EventType = "tool_call_end"
	EventError         EventType = "error"
	EventDone          EventType = "done"
)

// ToolEvent carries information about a tool invocation as it streams.
// Index is a stable 0-based counter assigned per-request, used to correlate
// start / delta / end events for the same tool call.
type ToolEvent struct {
	Index       int    `json:"index"`
	Name        string `json:"name,omitempty"`
	ID          string `json:"id,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	FinalInput  string `json:"final_input,omitempty"`
}

type AgentEvent struct {
	Type    EventType  `json:"type"`
	Content string     `json:"content,omitempty"`
	Tool    *ToolEvent `json:"tool,omitempty"`
	Error   string     `json:"error,omitempty"`
	// Populated on EventDone:
	StopReason   string  `json:"stop_reason,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

type Capabilities struct {
	Tools          bool
	FileEdit       bool
	Streaming      bool
	MaxContext     int
	SkillsCommands bool
}

// AgentRequest is the unified request handed to every adapter. Adapters use
// the fields they understand and ignore the rest.
type AgentRequest struct {
	Messages      []ChatMessage
	Model         string
	WorkspacePath string
	ClaudeConfig  string
	RequestID     string
	TimeoutSec    int

	SystemPrompt       string
	AppendSystemPrompt string
	Agent              string
	PermissionMode     string
	ClaudeModel        string
	Effort             string
	AllowedTools       []string
	DisallowedTools    []string
	MCPConfig          string
	AddDirs            []string
	Bare               bool
	Skill              string
	IncludeHookEvents  bool

	APIKey      string
	BaseURL     string
	MaxTokens   int
	Temperature float64

	// Claude OAuth subscription support: per-account credentials path and/or
	// full config dir. The claude-cli adapter symlinks these into the
	// per-request CLAUDE_CONFIG_DIR before spawning so the chosen
	// subscription's OAuth credential is used.
	ClaudeCredentialsPath string
	ClaudeConfigSourceDir string
}

type AgentAdapter interface {
	Name() string
	Available() bool
	Capabilities() Capabilities
	Run(ctx context.Context, req AgentRequest, events chan<- AgentEvent) error
}

var ErrNotImplemented = errors.New("adapter not implemented")
