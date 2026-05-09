package main

import (
	"strings"
	"testing"
)

// fixtureStream is a minimised stream-json transcript: thinking + Write tool
// call (with input_json deltas) + final text + result.
const fixtureStream = `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"x","tools":[],"mcp_servers":[],"model":"opus","permissionMode":"bypassPermissions","slash_commands":[],"apiKeySource":"none","claude_code_version":"2","output_style":"default","agents":[],"skills":[],"plugins":[],"analytics_disabled":false,"uuid":"a"}
{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1"}}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Write","input":{}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"hello.go\"}"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":1}}
{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Created"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":" hello.go"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":2}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":3000,"num_turns":2,"result":"Created hello.go","stop_reason":"end_turn","total_cost_usd":0.05,"usage":{},"uuid":"r"}
`

func TestParseClaudeStream_TextAndToolCalls(t *testing.T) {
	events := make(chan AgentEvent, 64)
	r := strings.NewReader(fixtureStream)
	if err := parseClaudeStream(r, events); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	close(events)

	var (
		gotText      strings.Builder
		toolStarts   []ToolEvent
		toolDeltas   []ToolEvent
		toolEnds     []ToolEvent
		doneEvent    *AgentEvent
	)
	for ev := range events {
		switch ev.Type {
		case EventTextDelta:
			gotText.WriteString(ev.Content)
		case EventToolCallStart:
			toolStarts = append(toolStarts, *ev.Tool)
		case EventToolCallDelta:
			toolDeltas = append(toolDeltas, *ev.Tool)
		case EventToolCallEnd:
			toolEnds = append(toolEnds, *ev.Tool)
		case EventDone:
			e := ev
			doneEvent = &e
		}
	}

	if got := gotText.String(); got != "Created hello.go" {
		t.Errorf("text = %q, want %q", got, "Created hello.go")
	}
	if len(toolStarts) != 1 {
		t.Fatalf("tool_call_start = %d, want 1", len(toolStarts))
	}
	if toolStarts[0].Name != "Write" || toolStarts[0].ID != "toolu_1" || toolStarts[0].Index != 0 {
		t.Errorf("tool_call_start = %+v, want Name=Write ID=toolu_1 Index=0", toolStarts[0])
	}
	if len(toolDeltas) != 2 {
		t.Fatalf("tool_call_delta = %d, want 2", len(toolDeltas))
	}
	combined := ""
	for _, d := range toolDeltas {
		if d.Index != 0 {
			t.Errorf("delta index = %d, want 0", d.Index)
		}
		combined += d.PartialJSON
	}
	if combined != `{"file_path":"hello.go"}` {
		t.Errorf("combined json = %q, want %q", combined, `{"file_path":"hello.go"}`)
	}
	if len(toolEnds) != 1 || toolEnds[0].Index != 0 {
		t.Errorf("tool_call_end = %+v, want one entry index 0", toolEnds)
	}
	if doneEvent == nil {
		t.Fatalf("no done event")
	}
	if doneEvent.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", doneEvent.StopReason)
	}
	if doneEvent.NumTurns != 2 {
		t.Errorf("num_turns = %d, want 2", doneEvent.NumTurns)
	}
	if doneEvent.CostUSD != 0.05 {
		t.Errorf("cost_usd = %v, want 0.05", doneEvent.CostUSD)
	}
}

func TestSplitModelSkill(t *testing.T) {
	cases := []struct {
		in        string
		wantBase  string
		wantSkill string
	}{
		{"claude-code-agent", "claude-code-agent", ""},
		{"claude-code-agent+brainstorming", "claude-code-agent", "brainstorming"},
		{"claude-sdk-agent+plan+more", "claude-sdk-agent", "plan+more"},
	}
	for _, c := range cases {
		gotBase, gotSkill := splitModelSkill(c.in)
		if gotBase != c.wantBase || gotSkill != c.wantSkill {
			t.Errorf("splitModelSkill(%q) = (%q,%q), want (%q,%q)",
				c.in, gotBase, gotSkill, c.wantBase, c.wantSkill)
		}
	}
}

func TestApplySkillPrefix(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "be helpful"},
		{Role: "user", Content: "hello"},
	}
	out := applySkillPrefix(msgs, "brainstorming")
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	if out[1].Content != "/brainstorming hello" {
		t.Errorf("content=%q want %q", out[1].Content, "/brainstorming hello")
	}
}
