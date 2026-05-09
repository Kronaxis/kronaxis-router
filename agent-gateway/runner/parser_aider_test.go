package runner

import (
	"strings"
	"testing"
)

func TestAiderParser_AssistantContent(t *testing.T) {
	body := strings.Join([]string{
		`{"role":"assistant","content":"Reading the file... "}`,
		`{"role":"assistant","content":"done."}`,
		`{"type":"complete","cost":0.0002,"input_tokens":10,"output_tokens":4}`,
	}, "\n")
	ch := make(chan Event, 8)
	if err := (AiderParser{}).Parse(strings.NewReader(body), ch); err != nil {
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
	if text.String() != "Reading the file... done." {
		t.Errorf("text=%q", text.String())
	}
	if done == nil || done.CostUSD != 0.0002 {
		t.Errorf("done=%+v", done)
	}
}

func TestAiderParser_EditEmitsToolCall(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"edit","file":"foo.py","summary":"add function bar"}`,
		`{"type":"complete"}`,
	}, "\n")
	ch := make(chan Event, 8)
	if err := (AiderParser{}).Parse(strings.NewReader(body), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	gotStart := false
	for e := range ch {
		if e.Type == EventToolCallStart && e.Tool != nil && e.Tool.Name == "edit_file" {
			gotStart = true
		}
	}
	if !gotStart {
		t.Error("expected edit_file tool call start")
	}
}
