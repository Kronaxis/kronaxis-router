package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

func init() { Register(&AiderParser{}) }

// AiderParser handles aider's scripting-mode output. When invoked with
// `--no-pretty --no-stream-cmd-output --yes-always --json`, aider emits a
// stream of JSONL events (best-effort — aider's output format is less
// formalised than claude/codex, so we accept what we recognise and pass
// through anything else as plain text).
//
// Recognised shapes:
//
//	{"role":"assistant","content":"..."}            -> text delta
//	{"type":"edit","file":"foo.py","summary":"..."} -> tool call
//	{"type":"complete","cost":0.001,"input_tokens":N,"output_tokens":N}
type AiderParser struct{}

func (AiderParser) Name() string { return "aider" }

type aiderLine struct {
	Role         string  `json:"role,omitempty"`
	Content      string  `json:"content,omitempty"`
	Type         string  `json:"type,omitempty"`
	File         string  `json:"file,omitempty"`
	Summary      string  `json:"summary,omitempty"`
	Cost         float64 `json:"cost,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
}

func (AiderParser) Parse(r io.Reader, events chan<- Event) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	nextToolIdx := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ev aiderLine
		if err := json.Unmarshal(raw, &ev); err != nil {
			events <- Event{Type: EventTextDelta, Content: string(raw) + "\n"}
			continue
		}
		switch {
		case ev.Role == "assistant" && ev.Content != "":
			events <- Event{Type: EventTextDelta, Content: ev.Content}
		case ev.Type == "edit":
			idx := nextToolIdx
			nextToolIdx++
			events <- Event{Type: EventToolCallStart, Tool: &ToolEvent{Index: idx, Name: "edit_file", ID: ev.File}}
			if ev.Summary != "" {
				events <- Event{Type: EventToolCallDelta, Tool: &ToolEvent{Index: idx, PartialJSON: ev.Summary}}
			}
			events <- Event{Type: EventToolCallEnd, Tool: &ToolEvent{Index: idx, Name: "edit_file"}}
		case ev.Type == "complete" || ev.Type == "done":
			stop := ev.StopReason
			if stop == "" {
				stop = "stop"
			}
			events <- Event{
				Type:         EventDone,
				StopReason:   stop,
				CostUSD:      ev.Cost,
				InputTokens:  ev.InputTokens,
				OutputTokens: ev.OutputTokens,
			}
			return nil
		case ev.Type == "error":
			events <- Event{Type: EventError, Error: ev.Content}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan stream: %w", err)
	}
	events <- Event{Type: EventDone, StopReason: "stream_closed"}
	return nil
}
