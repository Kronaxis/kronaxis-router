package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestMCPInitialize(t *testing.T) {
	srv := &MCPServer{routerURL: "http://localhost:8050"}
	id := json.RawMessage(`1`)
	resp := srv.handleInitialize(id)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(mcpInitResult)
	if !ok {
		t.Fatal("result is not mcpInitResult")
	}
	if result.ProtocolVersion != mcpProtocolVersion {
		t.Errorf("protocol version = %s, want %s", result.ProtocolVersion, mcpProtocolVersion)
	}
	if result.ServerInfo.Name != "kronaxis-router" {
		t.Errorf("server name = %s, want kronaxis-router", result.ServerInfo.Name)
	}
	if result.Capabilities.Tools == nil {
		t.Error("tools capability is nil")
	}
}

func TestMCPToolsList(t *testing.T) {
	srv := &MCPServer{routerURL: "http://localhost:8050"}
	id := json.RawMessage(`2`)
	resp := srv.handleToolsList(id)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result, ok := resp.Result.(mcpToolsResult)
	if !ok {
		t.Fatal("result is not mcpToolsResult")
	}

	if len(result.Tools) != 12 {
		t.Errorf("got %d tools, want 12", len(result.Tools))
	}

	// Check all expected tools are present
	expected := map[string]bool{
		"router_health":         false,
		"router_backends":       false,
		"router_costs":          false,
		"router_stats":          false,
		"router_rules":          false,
		"router_add_backend":    false,
		"router_remove_backend": false,
		"router_add_rule":       false,
		"router_remove_rule":    false,
		"router_update_budget":  false,
		"router_config":         false,
		"router_reload":         false,
	}

	for _, tool := range result.Tools {
		if _, ok := expected[tool.Name]; ok {
			expected[tool.Name] = true
		} else {
			t.Errorf("unexpected tool: %s", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %s has empty description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %s has nil inputSchema", tool.Name)
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing expected tool: %s", name)
		}
	}
}

func TestMCPToolCallUnknown(t *testing.T) {
	srv := &MCPServer{routerURL: "http://localhost:8050"}
	id := json.RawMessage(`3`)
	params, _ := json.Marshal(mcpToolCallParams{
		Name:      "nonexistent_tool",
		Arguments: map[string]interface{}{},
	})
	resp := srv.handleToolCall(id, params)

	if resp.Error != nil {
		t.Fatalf("should not return jsonrpc error for unknown tool")
	}

	result, ok := resp.Result.(mcpToolResult)
	if !ok {
		t.Fatal("result is not mcpToolResult")
	}
	if !result.IsError {
		t.Error("expected IsError=true for unknown tool")
	}
}

func TestMCPToolCallHealthNoRouter(t *testing.T) {
	// Calling health with no router running should return a graceful error
	srv := &MCPServer{
		routerURL:  "http://localhost:19999", // unlikely to be running
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
	id := json.RawMessage(`4`)
	params, _ := json.Marshal(mcpToolCallParams{
		Name:      "router_health",
		Arguments: map[string]interface{}{},
	})
	resp := srv.handleToolCall(id, params)

	result, ok := resp.Result.(mcpToolResult)
	if !ok {
		t.Fatal("result is not mcpToolResult")
	}
	if !result.IsError {
		t.Error("expected IsError=true when router is unreachable")
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected non-empty error message")
	}
}

func TestJSONSchemaBuilder(t *testing.T) {
	schema := jsonSchema("object", map[string]interface{}{
		"name": map[string]interface{}{"type": "string"},
	}, []string{"name"})

	if schema["type"] != "object" {
		t.Errorf("type = %v, want object", schema["type"])
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties missing")
	}
	if _, ok := props["name"]; !ok {
		t.Error("name property missing")
	}
	req, ok := schema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "name" {
		t.Error("required should be [name]")
	}
}

func TestInitSanitiseName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"llama3.1:8b", "llama3-1-8b"},
		{"meta/llama-3.1-70b", "meta-llama-3-1-70b"},
		{"qwen2.5:14b", "qwen2-5-14b"},
	}
	for _, tc := range tests {
		got := sanitiseName(tc.input)
		if got != tc.want {
			t.Errorf("sanitiseName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestInitPriorityForModel(t *testing.T) {
	tests := []struct {
		model    string
		minPri   int
	}{
		{"llama3.1:70b", 80},
		{"qwen2.5:14b", 40},
		{"phi3:3b", 1},
		{"unknown-model", 30},
	}
	for _, tc := range tests {
		got := priorityForModel(tc.model)
		if got < tc.minPri {
			t.Errorf("priorityForModel(%q) = %d, want >= %d", tc.model, got, tc.minPri)
		}
	}
}

func TestFormatResponseJSON(t *testing.T) {
	data := []byte(`{"status":"ok","count":5}`)
	result := formatResponse(200, data)
	if result.IsError {
		t.Error("expected IsError=false for 200")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content")
	}
	// Should be pretty-printed
	var parsed interface{}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsed); err != nil {
		t.Errorf("result should be valid JSON: %v", err)
	}
}

func TestFormatResponseError(t *testing.T) {
	result := formatResponse(500, []byte(`internal error`))
	if !result.IsError {
		t.Error("expected IsError=true for 500")
	}
}

