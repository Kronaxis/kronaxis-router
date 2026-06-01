package main

import "testing"

func newTestGate() *QualityGate {
	return newQualityGate(QualityGateConfig{}) // gate globally disabled
}

func TestShouldGateSchemaTriggersWhenDisabled(t *testing.T) {
	qg := newTestGate() // Enabled=false
	// No schema, gate disabled -> no gating.
	if qg.ShouldGate(RouteRequest{}) {
		t.Error("expected no gating with disabled gate + no schema")
	}
	// Schema present (non-stream) -> gate even though globally disabled.
	if !qg.ShouldGate(RouteRequest{ResponseSchema: `{"type":"object"}`}) {
		t.Error("a per-request schema should trigger gating regardless of enable flag")
	}
	// Schema present but streaming -> cannot validate, so no gating.
	if qg.ShouldGate(RouteRequest{ResponseSchema: `{"type":"object"}`, Stream: true}) {
		t.Error("streaming requests must not be gated even with a schema")
	}
}

func chatBody(content string) []byte {
	return []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":` +
		mustJSONString(content) + `}}]}`)
}

// mustJSONString returns content as a JSON string literal (quoted+escaped).
func mustJSONString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, string(r)...)
		}
	}
	out = append(out, '"')
	return string(out)
}

func TestSchemaValid(t *testing.T) {
	qg := newTestGate()
	schema := `{"type":"object","required":["name","score"],"properties":{"name":{"type":"string"},"score":{"type":"number"}}}`

	// No schema supplied -> always valid.
	if ok, _ := qg.schemaValid(RouteRequest{}, chatBody(`anything`)); !ok {
		t.Error("no schema should validate as ok")
	}

	// Cheap model returned schema-valid JSON.
	good := chatBody(`{"name":"alice","score":9.2}`)
	if ok, msg := qg.schemaValid(RouteRequest{ResponseSchema: schema}, good); !ok {
		t.Errorf("valid JSON should pass schema; got %s", msg)
	}

	// Missing required field -> violation.
	bad := chatBody(`{"name":"alice"}`)
	if ok, _ := qg.schemaValid(RouteRequest{ResponseSchema: schema}, bad); ok {
		t.Error("missing required field should fail schema")
	}

	// Not JSON at all -> violation.
	notJSON := chatBody(`Sure! Here is the answer.`)
	if ok, _ := qg.schemaValid(RouteRequest{ResponseSchema: schema}, notJSON); ok {
		t.Error("non-JSON content should fail schema")
	}
}
