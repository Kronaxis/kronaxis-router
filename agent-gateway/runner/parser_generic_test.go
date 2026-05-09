package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenericParser_LinesAndDone(t *testing.T) {
	body := "line one\nline two\nline three\n"
	ch := make(chan Event, 8)
	if err := (GenericParser{}).Parse(strings.NewReader(body), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	got := []string{}
	last := EventType("")
	for e := range ch {
		if e.Type == EventTextDelta {
			got = append(got, e.Content)
		}
		last = e.Type
	}
	want := []string{"line one\n", "line two\n", "line three\n"}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
	if last != EventDone {
		t.Errorf("last=%q, want Done", last)
	}
}

func TestGenericParser_BinaryLine(t *testing.T) {
	// Build an input where the first line is binary garbage and the second is text.
	bin := append([]byte{0xff, 0xfe, 0xfd}, []byte("\nhello\n")...)
	ch := make(chan Event, 8)
	if err := (GenericParser{}).Parse(bytes.NewReader(bin), ch); err != nil {
		t.Fatal(err)
	}
	close(ch)
	first := ""
	second := ""
	i := 0
	for e := range ch {
		if e.Type != EventTextDelta {
			continue
		}
		switch i {
		case 0:
			first = e.Content
		case 1:
			second = e.Content
		}
		i++
	}
	if !strings.HasPrefix(first, "[non-utf8]") {
		t.Errorf("first=%q, want non-utf8 prefix", first)
	}
	if second != "hello\n" {
		t.Errorf("second=%q, want %q", second, "hello\n")
	}
}
