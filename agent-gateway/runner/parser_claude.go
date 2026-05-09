package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

func init() { Register(&ClaudeParser{}) }

// ClaudeParser handles `claude -p --output-format stream-json` output.
//
// Stream events come as one JSON object per line. The shape we care about:
//
//	{"type":"stream_event","event":{"type":"content_block_start","index":0,
//	  "content_block":{"type":"text"|"tool_use","id":"...","name":"..."}}}
//	{"type":"stream_event","event":{"type":"content_block_delta","index":0,
//	  "delta":{"type":"text_delta","text":"..."} | {"type":"input_json_delta","partial_json":"..."}}}
//	{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
//	{"type":"result","stop_reason":"end_turn","num_turns":1,
//	  "total_cost_usd":0.012,"usage":{"input_tokens":10,"output_tokens":5}}
type ClaudeParser struct{}

func (ClaudeParser) Name() string { return "claude" }

type claudeStreamLine struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype,omitempty"`
	Event          *claudeInner    `json:"event,omitempty"`
	Result         string          `json:"result,omitempty"`
	NumTurns       int             `json:"num_turns,omitempty"`
	StopReason     string          `json:"stop_reason,omitempty"`
	TotalCostUSD   float64         `json:"total_cost_usd,omitempty"`
	Usage          *claudeUsage    `json:"usage,omitempty"`
	IsError        bool            `json:"is_error,omitempty"`
	APIErrorStatus json.RawMessage `json:"api_error_status,omitempty"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type claudeInner struct {
	Type         string       `json:"type"`
	Index        int          `json:"index,omitempty"`
	ContentBlock *claudeBlock `json:"content_block,omitempty"`
	Delta        *claudeDelta `json:"delta,omitempty"`
}

type claudeBlock struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type claudeDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

func (ClaudeParser) Parse(r io.Reader, events chan<- Event) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	type toolBlock struct {
		openAIIdx int
		name      string
		id        string
	}
	currentBlocks := map[int]*toolBlock{}
	nextOpenAIIdx := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev claudeStreamLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "stream_event":
			if ev.Event == nil {
				continue
			}
			switch ev.Event.Type {
			case "content_block_start":
				if ev.Event.ContentBlock != nil && ev.Event.ContentBlock.Type == "tool_use" {
					tb := &toolBlock{
						openAIIdx: nextOpenAIIdx,
						name:      ev.Event.ContentBlock.Name,
						id:        ev.Event.ContentBlock.ID,
					}
					nextOpenAIIdx++
					currentBlocks[ev.Event.Index] = tb
					events <- Event{
						Type: EventToolCallStart,
						Tool: &ToolEvent{Index: tb.openAIIdx, Name: tb.name, ID: tb.id},
					}
				}
			case "content_block_delta":
				if ev.Event.Delta == nil {
					continue
				}
				switch ev.Event.Delta.Type {
				case "text_delta":
					if ev.Event.Delta.Text != "" {
						events <- Event{Type: EventTextDelta, Content: ev.Event.Delta.Text}
					}
				case "input_json_delta":
					tb, ok := currentBlocks[ev.Event.Index]
					if !ok {
						continue
					}
					events <- Event{
						Type: EventToolCallDelta,
						Tool: &ToolEvent{Index: tb.openAIIdx, PartialJSON: ev.Event.Delta.PartialJSON},
					}
				}
			case "content_block_stop":
				if tb, ok := currentBlocks[ev.Event.Index]; ok {
					events <- Event{
						Type: EventToolCallEnd,
						Tool: &ToolEvent{Index: tb.openAIIdx, Name: tb.name},
					}
					delete(currentBlocks, ev.Event.Index)
				}
			}
		case "result":
			done := Event{
				Type:       EventDone,
				StopReason: ev.StopReason,
				NumTurns:   ev.NumTurns,
				CostUSD:    ev.TotalCostUSD,
			}
			if ev.Usage != nil {
				done.InputTokens = ev.Usage.InputTokens
				done.OutputTokens = ev.Usage.OutputTokens
			}
			events <- done
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan stream: %w", err)
	}
	events <- Event{Type: EventDone, StopReason: "stream_closed"}
	return nil
}
