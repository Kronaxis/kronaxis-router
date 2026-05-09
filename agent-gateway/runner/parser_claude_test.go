package runner

import (
	"strings"
	"testing"
)

func TestClaudeParser_TextDeltasAndDone(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"result","stop_reason":"end_turn","num_turns":1,"total_cost_usd":0.001,"usage":{"input_tokens":3,"output_tokens":2}}`,
	}, "\n")

	ch := make(chan Event, 32)
	if err := (ClaudeParser{}).Parse(strings.NewReader(stream), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	var text strings.Builder
	var done *Event
	for e := range ch {
		if e.Type == EventTextDelta {
			text.WriteString(e.Content)
		}
		if e.Type == EventDone {
			cp := e
			done = &cp
		}
	}
	if got := text.String(); got != "Hello world" {
		t.Errorf("text=%q, want %q", got, "Hello world")
	}
	if done == nil {
		t.Fatal("no Done event")
	}
	if done.StopReason != "end_turn" || done.InputTokens != 3 || done.OutputTokens != 2 {
		t.Errorf("done event wrong: %+v", *done)
	}
}

func TestClaudeParser_ToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"Read"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file\""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"foo\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"result","stop_reason":"tool_use"}`,
	}, "\n")
	ch := make(chan Event, 32)
	if err := (ClaudeParser{}).Parse(strings.NewReader(stream), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	starts, deltas, ends := 0, 0, 0
	for e := range ch {
		switch e.Type {
		case EventToolCallStart:
			starts++
		case EventToolCallDelta:
			deltas++
		case EventToolCallEnd:
			ends++
		}
	}
	if starts != 1 || deltas != 2 || ends != 1 {
		t.Errorf("starts=%d deltas=%d ends=%d, want 1/2/1", starts, deltas, ends)
	}
}

func TestClaudeParser_StreamClosedWithoutResult(t *testing.T) {
	stream := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}`
	ch := make(chan Event, 8)
	if err := (ClaudeParser{}).Parse(strings.NewReader(stream), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	last := EventType("")
	for e := range ch {
		last = e.Type
	}
	if last != EventDone {
		t.Errorf("last event = %q, want Done", last)
	}
}
