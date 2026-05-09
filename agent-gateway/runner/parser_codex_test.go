package runner

import (
	"strings"
	"testing"
)

func TestCodexParser_TextAndFinish(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"text_delta","content":"Hi "}`,
		`{"type":"text_delta","content":"there"}`,
		`{"type":"finish","stop_reason":"stop","usage":{"prompt_tokens":4,"completion_tokens":2,"total_cost_usd":0.0008}}`,
	}, "\n")
	ch := make(chan Event, 16)
	if err := (CodexParser{}).Parse(strings.NewReader(body), ch); err != nil {
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
	if text.String() != "Hi there" {
		t.Errorf("text=%q", text.String())
	}
	if done == nil || done.InputTokens != 4 || done.OutputTokens != 2 || done.CostUSD != 0.0008 {
		t.Errorf("done=%+v", done)
	}
}

func TestCodexParser_FallsBackToText(t *testing.T) {
	body := "this isn't json\n"
	ch := make(chan Event, 4)
	if err := (CodexParser{}).Parse(strings.NewReader(body), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	text := ""
	for e := range ch {
		if e.Type == EventTextDelta {
			text += e.Content
		}
	}
	if !strings.Contains(text, "this isn't json") {
		t.Errorf("expected fallback text, got %q", text)
	}
}

func TestCodexParser_ToolCall(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"tool_call","name":"read_file","id":"t1","arguments":"{\"path\":\"/x\"}"}`,
		`{"type":"finish","stop_reason":"stop"}`,
	}, "\n")
	ch := make(chan Event, 8)
	if err := (CodexParser{}).Parse(strings.NewReader(body), ch); err != nil {
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
	if starts != 1 || deltas != 1 || ends != 1 {
		t.Errorf("starts=%d deltas=%d ends=%d, want 1/1/1", starts, deltas, ends)
	}
}
