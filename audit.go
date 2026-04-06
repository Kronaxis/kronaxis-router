package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AuditLogger logs request/response pairs to a separate file with
// configurable PII redaction. Used for compliance and debugging
// without leaking sensitive data.

type AuditConfig struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	LogFile    string   `yaml:"log_file" json:"log_file"`
	Redact     []string `yaml:"redact" json:"redact"`           // regex patterns to redact
	MaxEntries int      `yaml:"max_entries" json:"max_entries"` // rotate after N entries (0=unlimited)
}

type AuditEntry struct {
	Timestamp  string `json:"timestamp"`
	Service    string `json:"service"`
	CallType   string `json:"call_type"`
	Priority   string `json:"priority"`
	Tier       int    `json:"tier"`
	Backend    string `json:"backend"`
	Rule       string `json:"rule"`
	StatusCode int    `json:"status_code"`
	LatencyMS  int64  `json:"latency_ms"`
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
	Cost       float64 `json:"cost"`
	Prompt     string `json:"prompt,omitempty"`
	Response   string `json:"response,omitempty"`
	Cached     bool   `json:"cached"`
}

type AuditLogger struct {
	config   AuditConfig
	file     *os.File
	patterns []*regexp.Regexp
	entries  int
	mu       sync.Mutex
}

func newAuditLogger(config AuditConfig) *AuditLogger {
	al := &AuditLogger{config: config}
	if !config.Enabled || config.LogFile == "" {
		return al
	}

	// Compile redaction patterns
	for _, pattern := range config.Redact {
		if re, err := regexp.Compile(pattern); err == nil {
			al.patterns = append(al.patterns, re)
		}
	}

	// Default redaction patterns (always active)
	defaults := []string{
		`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`,           // email
		`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`,                                      // US phone
		`\b(?:0\d{4}|\+44\s?\d{4})\s?\d{6}\b`,                                // UK phone
		`\b\d{3}-\d{2}-\d{4}\b`,                                              // SSN
		`\b(?:\d{4}[-\s]?){3}\d{4}\b`,                                        // credit card
		`\b[A-Z]{1,2}\d{1,2}\s?\d[A-Z]{2}\b`,                                // UK postcode
		`\b\d{5}(?:-\d{4})?\b`,                                               // US ZIP
		`\bsk-[a-zA-Z0-9]{20,}\b`,                                            // API keys (sk-...)
		`\bey[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\b`, // JWT tokens
	}
	for _, d := range defaults {
		if re, err := regexp.Compile(d); err == nil {
			al.patterns = append(al.patterns, re)
		}
	}

	f, err := os.OpenFile(config.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		logger.Printf("audit log open failed: %v", err)
		return al
	}
	al.file = f
	return al
}

// Log writes an audit entry with PII redaction.
func (al *AuditLogger) Log(entry AuditEntry) {
	if al.file == nil || !al.config.Enabled {
		return
	}

	// Redact PII
	entry.Prompt = al.redact(entry.Prompt)
	entry.Response = al.redact(entry.Response)

	al.mu.Lock()
	defer al.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	al.file.Write(data)
	al.file.Write([]byte("\n"))
	al.entries++

	// Rotate if max entries reached
	if al.config.MaxEntries > 0 && al.entries >= al.config.MaxEntries {
		al.rotate()
	}
}

func (al *AuditLogger) redact(s string) string {
	for _, re := range al.patterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

func (al *AuditLogger) rotate() {
	if al.file == nil {
		return
	}
	al.file.Close()
	ts := time.Now().Format("20060102-150405")
	newName := strings.TrimSuffix(al.config.LogFile, ".jsonl") + "-" + ts + ".jsonl"
	os.Rename(al.config.LogFile, newName)
	f, err := os.OpenFile(al.config.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		logger.Printf("audit log rotate failed: %v", err)
		return
	}
	al.file = f
	al.entries = 0
}

func (al *AuditLogger) Close() {
	al.mu.Lock()
	defer al.mu.Unlock()
	if al.file != nil {
		al.file.Close()
	}
}
