package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleSchema = `{
  "type": "object",
  "required": ["name", "score"],
  "properties": {
    "name":  {"type": "string", "minLength": 1},
    "score": {"type": "number", "minimum": 0, "maximum": 1}
  }
}`

func makeChatResponse(content string) []byte {
	body := map[string]any{
		"choices": []any{
			map[string]any{"message": map[string]any{"content": content}},
		},
	}
	b, _ := json.Marshal(body)
	return b
}

func TestSchemaValidator_ValidPasses(t *testing.T) {
	sv := NewSchemaValidator()
	resp := makeChatResponse(`{"name": "alice", "score": 0.42}`)
	ok, violation := sv.Validate(sampleSchema, resp)
	if !ok {
		t.Errorf("expected pass, got violation: %s", violation)
	}
}

func TestSchemaValidator_MissingRequiredFails(t *testing.T) {
	sv := NewSchemaValidator()
	resp := makeChatResponse(`{"name": "alice"}`)
	ok, violation := sv.Validate(sampleSchema, resp)
	if ok {
		t.Errorf("expected fail (missing required score)")
	}
	if !strings.Contains(strings.ToLower(violation), "score") {
		t.Errorf("violation should mention missing field, got: %s", violation)
	}
}

func TestSchemaValidator_WrongTypeFails(t *testing.T) {
	sv := NewSchemaValidator()
	resp := makeChatResponse(`{"name": "alice", "score": "high"}`)
	ok, _ := sv.Validate(sampleSchema, resp)
	if ok {
		t.Errorf("expected fail (score is string, not number)")
	}
}

func TestSchemaValidator_MalformedJSONFails(t *testing.T) {
	sv := NewSchemaValidator()
	resp := makeChatResponse(`not json`)
	ok, _ := sv.Validate(sampleSchema, resp)
	if ok {
		t.Errorf("expected fail (not valid JSON)")
	}
}

func TestSchemaValidator_EmptySchemaPasses(t *testing.T) {
	sv := NewSchemaValidator()
	resp := makeChatResponse(`{"anything": true}`)
	ok, _ := sv.Validate("", resp)
	if !ok {
		t.Errorf("empty schema should always pass")
	}
}

func TestSchemaValidator_CachesCompile(t *testing.T) {
	sv := NewSchemaValidator()
	resp := makeChatResponse(`{"name": "x", "score": 0.5}`)
	for i := 0; i < 100; i++ {
		ok, _ := sv.Validate(sampleSchema, resp)
		if !ok {
			t.Fatalf("iteration %d: should pass", i)
		}
	}
	// Cache holds exactly one entry (one distinct schema).
	if got := len(sv.cache); got != 1 {
		t.Errorf("cache size = %d, want 1", got)
	}
}

func TestDPOExporter_AppendsJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dpo.jsonl")
	exp, err := NewDPOExporter(path, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	for i := 0; i < 5; i++ {
		exp.Submit(DPOPair{
			Prompt:   "prompt",
			Rejected: "bad output",
			Chosen:   "good output",
			Metadata: map[string]interface{}{"i": i},
		})
	}
	// Give the writer goroutine time to flush.
	for i := 0; i < 50; i++ {
		if exp.Stats().Written == 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if exp.Stats().Written != 5 {
		t.Errorf("written = %d, want 5", exp.Stats().Written)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("wrote %d lines, want 5", len(lines))
	}
	for _, l := range lines {
		var p DPOPair
		if err := json.Unmarshal([]byte(l), &p); err != nil {
			t.Errorf("line not valid JSON: %v", err)
		}
		if p.Prompt == "" || p.Chosen == "" || p.Rejected == "" {
			t.Errorf("incomplete pair: %+v", p)
		}
	}
}

func TestDPOExporter_RedactsKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dpo.jsonl")
	exp, err := NewDPOExporter(path, 0, []string{"api_key", "password"})
	if err != nil {
		t.Fatal(err)
	}
	defer exp.Close()
	exp.Submit(DPOPair{
		Prompt:   "p",
		Rejected: "r",
		Chosen:   "c",
		Metadata: map[string]interface{}{
			"api_key":  "secret-12345",
			"password": "hunter2",
			"keep_me":  "this should remain",
		},
	})
	for i := 0; i < 50; i++ {
		if exp.Stats().Written >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "secret-12345") {
		t.Errorf("api_key leaked into DPO file: %s", string(body))
	}
	if strings.Contains(string(body), "hunter2") {
		t.Errorf("password leaked into DPO file: %s", string(body))
	}
	if !strings.Contains(string(body), "this should remain") {
		t.Errorf("non-redacted key was wrongly stripped: %s", string(body))
	}
}
