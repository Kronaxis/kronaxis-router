package main

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// AuditLogger emits one JSON line per logical event. Used for per-request
// audit trail and lifecycle events (sweep, startup, etc.).
type AuditLogger interface {
	Event(kind string, fields map[string]any)
	Request(r RequestRecord)
}

type RequestRecord struct {
	ID           string  `json:"id"`
	Model        string  `json:"model"`
	Adapter      string  `json:"adapter"`
	Stream       bool    `json:"stream"`
	Status       string  `json:"status"`
	HTTPStatus   int     `json:"http_status"`
	NumTurns     int     `json:"num_turns,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	DurationMS   int64   `json:"duration_ms"`
	Error        string  `json:"error,omitempty"`
	WorkspaceID  string  `json:"workspace_id,omitempty"`
	AccountID    string  `json:"account_id,omitempty"`
	Skill        string  `json:"skill,omitempty"`
	UserAgent    string  `json:"user_agent,omitempty"`
	RemoteAddr   string  `json:"remote_addr,omitempty"`
}

type jsonAudit struct {
	w  io.Writer
	mu sync.Mutex
}

func newJSONAudit(w io.Writer) *jsonAudit {
	return &jsonAudit{w: w}
}

func (j *jsonAudit) Event(kind string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = kind
	fields["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	j.write(fields)
}

func (j *jsonAudit) Request(r RequestRecord) {
	j.mu.Lock()
	defer j.mu.Unlock()
	type wrapped struct {
		Event string `json:"event"`
		TS    string `json:"ts"`
		RequestRecord
	}
	w := wrapped{
		Event:         "request",
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		RequestRecord: r,
	}
	enc := json.NewEncoder(j.w)
	_ = enc.Encode(w)
}

func (j *jsonAudit) write(v any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	enc := json.NewEncoder(j.w)
	_ = enc.Encode(v)
}

// defaultAuditWriter resolves where audit JSON lines go. Defaults to stderr.
// Settable via AGENT_GATEWAY_AUDIT_FILE env var.
func defaultAuditWriter() io.Writer {
	if path := os.Getenv("AGENT_GATEWAY_AUDIT_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			return f
		}
	}
	return os.Stderr
}
