package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

func init() { Register(&CodexParser{}) }

// CodexParser handles OpenAI Codex CLI's `--json` output.
//
// Codex emits JSONL where each line is a structured event. The shapes we
// care about (best-effort — we accept what we recognise and silently skip
// what we don't):
//
//	{"type":"text_delta","content":"..."}
//	{"type":"tool_call","name":"...","id":"...","arguments":"..."}
//	{"type":"finish","stop_reason":"stop","usage":{"prompt_tokens":N,"completion_tokens":N}}
//	{"type":"error","message":"..."}
//
// When Codex changes its event shape, pin the parser tests to a captured
// sample and update accordingly. The shape above is what we observed on
// codex-cli 0.x; the plain-text fallback below catches anything that
// failed to parse so the stream never silently swallows output.
type CodexParser struct{}

func (CodexParser) Name() string { return "codex" }

type codexLine struct {
	Type        string         `json:"type"`
	Content     string         `json:"content,omitempty"`
	Text        string         `json:"text,omitempty"`
	Name        string         `json:"name,omitempty"`
	ID          string         `json:"id,omitempty"`
	Arguments   string         `json:"arguments,omitempty"`
	StopReason  string         `json:"stop_reason,omitempty"`
	FinishReason string        `json:"finish_reason,omitempty"`
	Message     string         `json:"message,omitempty"`
	Usage       *codexUsage    `json:"usage,omitempty"`
	NumTurns    int            `json:"num_turns,omitempty"`
}

type codexUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd,omitempty"`
}

func (CodexParser) Parse(r io.Reader, events chan<- Event) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	nextToolIdx := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ev codexLine
		if err := json.Unmarshal(raw, &ev); err != nil {
			// Plain text fallback so we never silently drop output.
			events <- Event{Type: EventTextDelta, Content: string(raw) + "\n"}
			continue
		}
		switch ev.Type {
		case "text_delta", "assistant", "message":
			content := ev.Content
			if content == "" {
				content = ev.Text
			}
			if content != "" {
				events <- Event{Type: EventTextDelta, Content: content}
			}
		case "tool_call":
			idx := nextToolIdx
			nextToolIdx++
			events <- Event{Type: EventToolCallStart, Tool: &ToolEvent{Index: idx, Name: ev.Name, ID: ev.ID}}
			if ev.Arguments != "" {
				events <- Event{Type: EventToolCallDelta, Tool: &ToolEvent{Index: idx, PartialJSON: ev.Arguments}}
			}
			events <- Event{Type: EventToolCallEnd, Tool: &ToolEvent{Index: idx, Name: ev.Name}}
		case "finish", "result", "done":
			done := Event{
				Type:       EventDone,
				StopReason: firstNonEmpty(ev.StopReason, ev.FinishReason, "stop"),
				NumTurns:   ev.NumTurns,
			}
			if ev.Usage != nil {
				done.InputTokens = ev.Usage.PromptTokens
				done.OutputTokens = ev.Usage.CompletionTokens
				done.CostUSD = ev.Usage.TotalCostUSD
			}
			events <- done
			return nil
		case "error":
			events <- Event{Type: EventError, Error: ev.Message}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan stream: %w", err)
	}
	events <- Event{Type: EventDone, StopReason: "stream_closed"}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
