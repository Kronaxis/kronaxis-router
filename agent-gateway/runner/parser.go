package runner

import (
	"fmt"
	"io"
	"sync"
)

// Parser decodes a CLI subprocess's stdout into a stream of Events.
//
// Implementations MUST be safe to call concurrently from different goroutines
// (each Parse call gets its own reader). They MUST NOT close the events
// channel — the runner owns the lifecycle.
type Parser interface {
	Name() string
	Parse(r io.Reader, events chan<- Event) error
}

var (
	parsersMu sync.RWMutex
	parsers   = map[string]Parser{}
)

// Register a parser by name. Built-in parsers register in init().
func Register(p Parser) {
	if p == nil || p.Name() == "" {
		return
	}
	parsersMu.Lock()
	defer parsersMu.Unlock()
	parsers[p.Name()] = p
}

// LookupParser fetches a parser by name. Returns an error if unknown.
func LookupParser(name string) (Parser, error) {
	parsersMu.RLock()
	defer parsersMu.RUnlock()
	if p, ok := parsers[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown parser %q", name)
}

// ParserNames returns registered parser names.
func ParserNames() []string {
	parsersMu.RLock()
	defer parsersMu.RUnlock()
	out := make([]string, 0, len(parsers))
	for n := range parsers {
		out = append(out, n)
	}
	return out
}
