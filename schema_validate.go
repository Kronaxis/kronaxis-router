package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// SchemaValidator compiles JSON Schemas on first sight and caches the
// compiled instance keyed by schema-content hash. Subsequent validations
// skip the parse + compile cost.
//
// Caller usage:
//   sv := NewSchemaValidator()
//   ok, violation := sv.Validate(schemaJSON, responseJSON)
//
// Lookup is O(1); compile is O(schema-size) and happens once per
// distinct schema string. Cache is bounded by `maxSchemas` (default
// 1024) with random eviction when full.
type SchemaValidator struct {
	mu         sync.RWMutex
	cache      map[uint64]*jsonschema.Schema
	maxSchemas int
}

// NewSchemaValidator returns an empty validator with a 1024-entry cache.
func NewSchemaValidator() *SchemaValidator {
	return &SchemaValidator{
		cache:      map[uint64]*jsonschema.Schema{},
		maxSchemas: 1024,
	}
}

// Validate compiles or fetches the schema, then validates the response.
// Returns (true, "") on pass, (false, violation-message) on fail. Errors
// in the schema itself surface as a validation failure with the parse
// error in the message; the caller can decide what to do.
//
// `responseBody` may be the raw OpenAI chat-completion response (a
// JSON object with a `choices[0].message.content` field). Validate
// extracts the content automatically and validates that against the
// schema. If extraction fails the response is treated as a violation.
func (sv *SchemaValidator) Validate(schemaJSON string, responseBody []byte) (bool, string) {
	if strings.TrimSpace(schemaJSON) == "" {
		return true, "" // nothing to validate against
	}
	schema, err := sv.compile(schemaJSON)
	if err != nil {
		return false, "schema compile: " + err.Error()
	}
	content, ok := extractMessageContent(responseBody)
	if !ok {
		return false, "could not extract message.content from response"
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return false, "content is not valid JSON: " + err.Error()
	}
	if err := schema.Validate(parsed); err != nil {
		return false, summariseValidationError(err)
	}
	return true, ""
}

func (sv *SchemaValidator) compile(schemaJSON string) (*jsonschema.Schema, error) {
	key := fnvHash(schemaJSON)
	sv.mu.RLock()
	if s, ok := sv.cache[key]; ok {
		sv.mu.RUnlock()
		return s, nil
	}
	sv.mu.RUnlock()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("inline.json", strings.NewReader(schemaJSON)); err != nil {
		return nil, err
	}
	s, err := c.Compile("inline.json")
	if err != nil {
		return nil, err
	}
	sv.mu.Lock()
	if len(sv.cache) >= sv.maxSchemas {
		for k := range sv.cache {
			delete(sv.cache, k)
			break
		}
	}
	sv.cache[key] = s
	sv.mu.Unlock()
	return s, nil
}

// extractMessageContent pulls choices[0].message.content from an OpenAI
// chat-completion response. Returns ok=false if the structure isn't
// what we expect.
func extractMessageContent(body []byte) (string, bool) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", false
	}
	if len(resp.Choices) == 0 {
		return "", false
	}
	return resp.Choices[0].Message.Content, true
}

// summariseValidationError flattens a jsonschema.ValidationError into a
// human-readable single-line message suitable for logs and DPO metadata.
func summariseValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	var b strings.Builder
	b.WriteString(ve.Message)
	for _, cause := range ve.Causes {
		b.WriteString("; ")
		b.WriteString(cause.Message)
	}
	return b.String()
}

func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, s)
	return h.Sum64()
}

// SchemaValidationError is returned by helpers that need to distinguish
// schema-violation failures from transport / backend errors.
type SchemaValidationError struct {
	Violation string
}

func (e *SchemaValidationError) Error() string {
	return fmt.Sprintf("schema violation: %s", e.Violation)
}
