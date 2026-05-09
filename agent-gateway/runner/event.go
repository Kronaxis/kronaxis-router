package runner

// EventType classifies a parser-emitted event.
type EventType string

const (
	EventTextDelta     EventType = "text_delta"
	EventToolCallStart EventType = "tool_call_start"
	EventToolCallDelta EventType = "tool_call_delta"
	EventToolCallEnd   EventType = "tool_call_end"
	EventError         EventType = "error"
	EventDone          EventType = "done"
)

// ToolEvent carries information about a streaming tool invocation.
type ToolEvent struct {
	Index       int    `json:"index"`
	Name        string `json:"name,omitempty"`
	ID          string `json:"id,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	FinalInput  string `json:"final_input,omitempty"`
}

// Event is a single piece of parser output.
type Event struct {
	Type         EventType  `json:"type"`
	Content      string     `json:"content,omitempty"`
	Tool         *ToolEvent `json:"tool,omitempty"`
	Error        string     `json:"error,omitempty"`
	StopReason   string     `json:"stop_reason,omitempty"`
	NumTurns     int        `json:"num_turns,omitempty"`
	CostUSD      float64    `json:"cost_usd,omitempty"`
	InputTokens  int        `json:"input_tokens,omitempty"`
	OutputTokens int        `json:"output_tokens,omitempty"`
}
