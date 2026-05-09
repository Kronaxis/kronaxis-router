package runner

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"unicode/utf8"
)

func init() { Register(&GenericParser{}) }

// GenericParser is the supported-tier fallback. It treats stdout as a stream
// of UTF-8 text and emits a text delta per line (preserving the trailing
// newline). On EOF it emits a single Done event. On invalid UTF-8 mid-stream
// it hex-escapes the bytes so the stream never breaks.
type GenericParser struct{}

func (GenericParser) Name() string { return "generic" }

func (GenericParser) Parse(r io.Reader, events chan<- Event) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text() // bufio.Scanner strips the newline; we add it back below
		if utf8.ValidString(line) {
			events <- Event{Type: EventTextDelta, Content: line + "\n"}
		} else {
			events <- Event{Type: EventTextDelta, Content: "[non-utf8]" + hex.EncodeToString(scanner.Bytes()) + "\n"}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan stream: %w", err)
	}
	events <- Event{Type: EventDone, StopReason: "stop"}
	return nil
}
