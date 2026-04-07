package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// MCP (Model Context Protocol) server over stdio.
// Connects to a running kronaxis-router HTTP API as a thin client.
// Used by Claude Code, Claude Desktop, Cursor, and other MCP-compatible tools.

const mcpProtocolVersion = "2024-11-05"

// JSON-RPC types
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // can be int or string; omit for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// MCP types
type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpCapabilities struct {
	Tools *struct{} `json:"tools,omitempty"`
}

type mcpInitResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    mcpCapabilities `json:"capabilities"`
	ServerInfo      mcpServerInfo   `json:"serverInfo"`
}

type mcpTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type mcpToolsResult struct {
	Tools []mcpTool `json:"tools"`
}

type mcpToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPServer handles the MCP protocol over stdio.
type MCPServer struct {
	routerURL  string
	apiToken   string
	httpClient *http.Client
}

func runMCP(_ []string) {
	routerURL := os.Getenv("ROUTER_URL")
	if routerURL == "" {
		routerURL = "http://localhost:8050"
	}
	// Strip trailing slash
	routerURL = strings.TrimRight(routerURL, "/")

	token := os.Getenv("ROUTER_API_TOKEN")

	srv := &MCPServer{
		routerURL:  routerURL,
		apiToken:   token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}

	// Log to stderr (stdout is the MCP channel)
	mcpLog := func(format string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, "[mcp] "+format+"\n", args...)
	}

	mcpLog("kronaxis-router MCP server starting (router: %s)", routerURL)

	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer for large messages
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			mcpLog("parse error: %v", err)
			continue
		}

		var resp *jsonRPCResponse

		switch req.Method {
		case "initialize":
			resp = srv.handleInitialize(req.ID)
		case "notifications/initialized":
			// Notification, no response needed
			mcpLog("client initialized")
			continue
		case "tools/list":
			resp = srv.handleToolsList(req.ID)
		case "tools/call":
			resp = srv.handleToolCall(req.ID, req.Params)
		case "ping":
			resp = &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
		default:
			resp = &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method},
			}
		}

		if resp != nil {
			out, _ := json.Marshal(resp)
			fmt.Fprintf(os.Stdout, "%s\n", out)
		}
	}

	if err := scanner.Err(); err != nil {
		mcpLog("stdin error: %v", err)
	}
}

func (s *MCPServer) handleInitialize(id json.RawMessage) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: mcpInitResult{
			ProtocolVersion: mcpProtocolVersion,
			Capabilities:    mcpCapabilities{Tools: &struct{}{}},
			ServerInfo:      mcpServerInfo{Name: "kronaxis-router", Version: version},
		},
	}
}

func (s *MCPServer) handleToolsList(id json.RawMessage) *jsonRPCResponse {
	tools := []mcpTool{
		{
			Name:        "router_health",
			Description: "Check router health: backend statuses, uptime, cache and quality stats. Use this to see which LLM backends are up/down and the overall system state.",
			InputSchema: jsonSchema("object", nil, nil),
		},
		{
			Name:        "router_backends",
			Description: "List all registered LLM backends with their health status, type, URL, active requests, latency, and cost per 1M tokens.",
			InputSchema: jsonSchema("object", nil, nil),
		},
		{
			Name:        "router_costs",
			Description: "View today's LLM spending broken down by service, model, and call type. Shows daily budget limits and current usage. Pass period='week' or 'month' for longer ranges.",
			InputSchema: jsonSchema("object", map[string]interface{}{
				"period": map[string]interface{}{
					"type":        "string",
					"description": "Time period: 'today', 'week', or 'month'",
					"enum":        []string{"today", "week", "month"},
				},
			}, nil),
		},
		{
			Name:        "router_stats",
			Description: "Get live request statistics: total requests, active requests, errors, average latency, requests by rule/service/model.",
			InputSchema: jsonSchema("object", nil, nil),
		},
		{
			Name:        "router_rules",
			Description: "List all routing rules showing priority, match criteria, backend chain, and cost ceiling.",
			InputSchema: jsonSchema("object", nil, nil),
		},
		{
			Name:        "router_add_backend",
			Description: "Register a new LLM backend. Provide name, URL, type (vllm/ollama/gemini/openai), model name, and optionally costs and capabilities.",
			InputSchema: jsonSchema("object", map[string]interface{}{
				"name":          map[string]interface{}{"type": "string", "description": "Unique backend identifier"},
				"url":           map[string]interface{}{"type": "string", "description": "Backend URL (e.g. http://localhost:11434)"},
				"type":          map[string]interface{}{"type": "string", "description": "Backend type", "enum": []string{"vllm", "ollama", "gemini", "openai"}},
				"model_name":    map[string]interface{}{"type": "string", "description": "Model name at this backend"},
				"cost_input_1m": map[string]interface{}{"type": "number", "description": "Cost per 1M input tokens (USD)"},
				"cost_output_1m": map[string]interface{}{"type": "number", "description": "Cost per 1M output tokens (USD)"},
				"max_concurrent": map[string]interface{}{"type": "integer", "description": "Max concurrent requests (default 10)"},
				"api_key":       map[string]interface{}{"type": "string", "description": "API key (or env:VAR_NAME)"},
			}, []string{"name", "url", "type", "model_name"}),
		},
		{
			Name:        "router_remove_backend",
			Description: "Remove a registered LLM backend by name.",
			InputSchema: jsonSchema("object", map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Backend name to remove"},
			}, []string{"name"}),
		},
		{
			Name:        "router_add_rule",
			Description: "Create a new routing rule. Rules match on service, call_type, tier (1=heavy, 2=light), priority_level (interactive/normal/background/bulk), and route to specified backends.",
			InputSchema: jsonSchema("object", map[string]interface{}{
				"name":     map[string]interface{}{"type": "string", "description": "Unique rule name"},
				"priority": map[string]interface{}{"type": "integer", "description": "Rule priority (higher evaluated first, e.g. 100-200)"},
				"match": map[string]interface{}{
					"type":        "object",
					"description": "Match criteria (all optional, empty = match everything)",
					"properties": map[string]interface{}{
						"service":        map[string]interface{}{"type": "string"},
						"call_type":      map[string]interface{}{"type": "string"},
						"tier":           map[string]interface{}{"type": "integer"},
						"priority_level": map[string]interface{}{"type": "string"},
					},
				},
				"backends":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Backend names in failover order"},
				"max_cost_1m": map[string]interface{}{"type": "number", "description": "Max cost per 1M tokens (filters expensive backends)"},
			}, []string{"name", "priority", "backends"}),
		},
		{
			Name:        "router_remove_rule",
			Description: "Remove a routing rule by name.",
			InputSchema: jsonSchema("object", map[string]interface{}{
				"name": map[string]interface{}{"type": "string", "description": "Rule name to remove"},
			}, []string{"name"}),
		},
		{
			Name:        "router_update_budget",
			Description: "Update per-service daily budget. Set daily limit, action on exceed (downgrade to cheaper model or reject), and downgrade target backend.",
			InputSchema: jsonSchema("object", map[string]interface{}{
				"service":          map[string]interface{}{"type": "string", "description": "Service name (or 'default' for catch-all)"},
				"daily_limit_usd":  map[string]interface{}{"type": "number", "description": "Daily spending limit in USD"},
				"action":           map[string]interface{}{"type": "string", "description": "Action on exceed", "enum": []string{"downgrade", "reject"}},
				"downgrade_target": map[string]interface{}{"type": "string", "description": "Backend to downgrade to (when action=downgrade)"},
			}, []string{"service", "daily_limit_usd", "action"}),
		},
		{
			Name:        "router_config",
			Description: "Get the current router configuration as YAML. Shows all backends, rules, budgets, rate limits, and batching settings.",
			InputSchema: jsonSchema("object", nil, nil),
		},
		{
			Name:        "router_reload",
			Description: "Force the router to reload its configuration from disk. Use after manually editing config.yaml.",
			InputSchema: jsonSchema("object", nil, nil),
		},
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  mcpToolsResult{Tools: tools},
	}
}

func (s *MCPServer) handleToolCall(id json.RawMessage, params json.RawMessage) *jsonRPCResponse {
	var call mcpToolCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &jsonRPCError{Code: -32602, Message: "invalid params: " + err.Error()},
		}
	}

	var result mcpToolResult

	switch call.Name {
	case "router_health":
		result = s.callGet("/health")
	case "router_backends":
		result = s.callGet("/api/backends")
	case "router_costs":
		period := "today"
		if p, ok := call.Arguments["period"].(string); ok {
			period = p
		}
		result = s.callGet("/api/costs?period=" + period)
	case "router_stats":
		result = s.callGet("/api/stats")
	case "router_rules":
		result = s.callGet("/api/rules")
	case "router_add_backend":
		result = s.callPost("/api/backends", call.Arguments)
	case "router_remove_backend":
		name, _ := call.Arguments["name"].(string)
		result = s.callDelete("/api/backends?name=" + name)
	case "router_add_rule":
		result = s.callPost("/api/rules", call.Arguments)
	case "router_remove_rule":
		name, _ := call.Arguments["name"].(string)
		result = s.callDelete("/api/rules?name=" + name)
	case "router_update_budget":
		result = s.handleUpdateBudget(call.Arguments)
	case "router_config":
		result = s.callGetRaw("/api/config/yaml")
	case "router_reload":
		result = s.callPostEmpty("/api/config/reload")
	default:
		result = mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Unknown tool: " + call.Name}},
			IsError: true,
		}
	}

	return &jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
}

// HTTP helpers that call the running router API

func (s *MCPServer) doRequest(method, path string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequest(method, s.routerURL+path, body)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiToken)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func (s *MCPServer) callGet(path string) mcpToolResult {
	status, data, err := s.doRequest("GET", path, nil)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error connecting to router at " + s.routerURL + ": " + err.Error()}},
			IsError: true,
		}
	}
	return formatResponse(status, data)
}

func (s *MCPServer) callGetRaw(path string) mcpToolResult {
	status, data, err := s.doRequest("GET", path, nil)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error connecting to router: " + err.Error()}},
			IsError: true,
		}
	}
	if status >= 400 {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("HTTP %d: %s", status, string(data))}},
			IsError: true,
		}
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

func (s *MCPServer) callPost(path string, body interface{}) mcpToolResult {
	data, _ := json.Marshal(body)
	status, respData, err := s.doRequest("POST", path, strings.NewReader(string(data)))
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error connecting to router: " + err.Error()}},
			IsError: true,
		}
	}
	return formatResponse(status, respData)
}

func (s *MCPServer) callPostEmpty(path string) mcpToolResult {
	status, data, err := s.doRequest("POST", path, nil)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error connecting to router: " + err.Error()}},
			IsError: true,
		}
	}
	return formatResponse(status, data)
}

func (s *MCPServer) callDelete(path string) mcpToolResult {
	status, data, err := s.doRequest("DELETE", path, nil)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error connecting to router: " + err.Error()}},
			IsError: true,
		}
	}
	return formatResponse(status, data)
}

func (s *MCPServer) handleUpdateBudget(args map[string]interface{}) mcpToolResult {
	// First get existing budgets
	_, existing, err := s.doRequest("GET", "/api/budgets", nil)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error reading budgets: " + err.Error()}},
			IsError: true,
		}
	}

	var budgets map[string]interface{}
	json.Unmarshal(existing, &budgets)
	if budgets == nil {
		budgets = make(map[string]interface{})
	}

	service, _ := args["service"].(string)
	budgets[service] = map[string]interface{}{
		"daily_limit_usd":  args["daily_limit_usd"],
		"action":           args["action"],
		"downgrade_target": args["downgrade_target"],
	}

	data, _ := json.Marshal(budgets)
	status, respData, err := s.doRequest("PUT", "/api/budgets", strings.NewReader(string(data)))
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error updating budgets: " + err.Error()}},
			IsError: true,
		}
	}
	return formatResponse(status, respData)
}

func formatResponse(status int, data []byte) mcpToolResult {
	if status >= 400 {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("HTTP %d: %s", status, string(data))}},
			IsError: true,
		}
	}

	// Pretty-print JSON
	var parsed interface{}
	if err := json.Unmarshal(data, &parsed); err == nil {
		pretty, err := json.MarshalIndent(parsed, "", "  ")
		if err == nil {
			return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(pretty)}}}
		}
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

// jsonSchema builds a JSON Schema object for MCP tool input definitions.
func jsonSchema(typ string, properties map[string]interface{}, required []string) map[string]interface{} {
	schema := map[string]interface{}{
		"type": typ,
	}
	if properties != nil {
		schema["properties"] = properties
	}
	if required != nil {
		schema["required"] = required
	}
	return schema
}
