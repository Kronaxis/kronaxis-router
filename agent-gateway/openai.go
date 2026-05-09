package main

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest is OpenAI-compatible. Extra non-standard fields are
// kronaxis pass-through controls; OpenAI clients ignore them. They map to
// `claude` CLI flags or to gateway-level concerns (workspaces, skills).
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	User        string        `json:"user,omitempty"`

	// ── Kronaxis extensions (non-standard, optional) ──
	SystemPrompt       string   `json:"system_prompt,omitempty"`
	AppendSystemPrompt string   `json:"append_system_prompt,omitempty"`
	Agent              string   `json:"agent,omitempty"`
	PermissionMode     string   `json:"permission_mode,omitempty"`
	ClaudeModel        string   `json:"claude_model,omitempty"`
	Effort             string   `json:"effort,omitempty"`
	AllowedTools       []string `json:"allowed_tools,omitempty"`
	DisallowedTools    []string `json:"disallowed_tools,omitempty"`
	MCPConfig          string   `json:"mcp_config,omitempty"`
	AddDirs            []string `json:"add_dirs,omitempty"`
	Bare               bool     `json:"bare,omitempty"`
	Skill              string   `json:"skill,omitempty"`
	WorkspaceID        string   `json:"workspace_id,omitempty"`
	BaseRepo           string   `json:"base_repo,omitempty"`
	IncludeHookEvents  bool     `json:"include_hook_events,omitempty"`
	AccountID          string   `json:"account_id,omitempty"` // pin to a specific named account from auth_pool
}

type Choice struct {
	Index        int          `json:"index"`
	Message      *AssistantMessage `json:"message,omitempty"`
	Delta        *Delta       `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason,omitempty"`
}

// Delta is the per-chunk delta in a streaming response.
type Delta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []ToolCallDelta `json:"tool_calls,omitempty"`
}

// ToolCallDelta matches the OpenAI streaming shape for tool calls.
// On the first chunk for a given index: id + type + function.name are populated.
// On subsequent chunks: only function.arguments accumulates as a JSON string.
type ToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *ToolFunctionDelta `json:"function,omitempty"`
}

type ToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolCall is the buffered (non-streaming) OpenAI shape.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// AssistantMessage is the buffered (non-streaming) message shape.
type AssistantMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

type ChatCompletionResponse struct {
	ID       string          `json:"id"`
	Object   string          `json:"object"`
	Created  int64           `json:"created"`
	Model    string          `json:"model"`
	Choices  []Choice        `json:"choices"`
	Usage    *Usage          `json:"usage,omitempty"`
	Kronaxis *KronaxisExtras `json:"kronaxis,omitempty"`
}

type ChatCompletionChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

type KronaxisExtras struct {
	WorkspacePath string `json:"workspace_path,omitempty"`
	GitDiff       string `json:"git_diff,omitempty"`
	NumTurns      int    `json:"num_turns,omitempty"`
	StopReason    string `json:"stop_reason,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	Adapter       string `json:"adapter,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	AccountID     string `json:"account_id,omitempty"`
}

type ModelInfo struct {
	ID        string   `json:"id"`
	Object    string   `json:"object"`
	OwnedBy   string   `json:"owned_by"`
	Available bool     `json:"available"`
	Adapter   string   `json:"adapter"`
}

type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}
