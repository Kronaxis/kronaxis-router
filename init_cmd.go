package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runInit auto-detects local models and API keys, generates config.yaml,
// and prints integration instructions for the target tool.
func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	flagAider := fs.Bool("aider", false, "Print Aider integration instructions")
	flagContinue := fs.Bool("continue", false, "Print Continue.dev integration instructions")
	flagOpenWebUI := fs.Bool("openwebui", false, "Print Open WebUI integration instructions")
	flagCursor := fs.Bool("cursor", false, "Print Cursor MCP integration instructions")
	flagClaude := fs.Bool("claude", false, "Configure Claude Code MCP server")
	flagPort := fs.Int("port", 8050, "Router listen port")
	flagOutput := fs.String("output", "config.yaml", "Config output path")
	fs.Parse(args)

	fmt.Println("kronaxis-router init: detecting local environment")
	fmt.Println()

	var backends []initBackend
	var envKeys []string

	// Detect Ollama
	if models := detectOllama(); len(models) > 0 {
		fmt.Printf("  Found Ollama with %d model(s): %s\n", len(models), strings.Join(models, ", "))
		for _, m := range models {
			backends = append(backends, initBackend{
				name:     "ollama-" + sanitiseName(m),
				url:      "http://localhost:11434",
				typ:      "ollama",
				model:    m,
				costIn:   0.0,
				costOut:  0.0,
				caps:     []string{"json_output"},
				maxConc:  4,
				priority: priorityForModel(m),
			})
		}
	} else {
		fmt.Println("  Ollama: not detected (localhost:11434)")
	}

	// Detect vLLM
	if models := detectVLLM("http://localhost:8000"); len(models) > 0 {
		fmt.Printf("  Found vLLM with %d model(s): %s\n", len(models), strings.Join(models, ", "))
		for _, m := range models {
			backends = append(backends, initBackend{
				name:     "vllm-" + sanitiseName(m),
				url:      "http://localhost:8000",
				typ:      "vllm",
				model:    m,
				costIn:   0.01,
				costOut:  0.01,
				caps:     []string{"json_output"},
				maxConc:  8,
				priority: priorityForModel(m),
			})
		}
	} else {
		fmt.Println("  vLLM: not detected (localhost:8000)")
	}

	// Detect cloud API keys
	cloudProviders := []struct {
		envVar string
		name   string
		typ    string
		url    string
		model  string
		costIn float64
		costOut float64
	}{
		{"GEMINI_API_KEY", "gemini", "gemini", "https://generativelanguage.googleapis.com", "gemini-2.5-flash", 0.15, 0.60},
		{"OPENAI_API_KEY", "openai", "openai", "https://api.openai.com", "gpt-4o-mini", 0.15, 0.60},
		{"ANTHROPIC_API_KEY", "anthropic", "openai", "https://api.anthropic.com", "claude-sonnet-4-20250514", 3.00, 15.00},
		{"GROQ_API_KEY", "groq", "openai", "https://api.groq.com/openai", "llama-3.3-70b-versatile", 0.59, 0.79},
		{"TOGETHER_API_KEY", "together", "openai", "https://api.together.xyz", "meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo", 0.88, 0.88},
		{"FIREWORKS_API_KEY", "fireworks", "openai", "https://api.fireworks.ai/inference", "accounts/fireworks/models/llama-v3p1-70b-instruct", 0.90, 0.90},
	}

	for _, p := range cloudProviders {
		if key := os.Getenv(p.envVar); key != "" {
			fmt.Printf("  Found %s (%s)\n", p.envVar, p.name)
			envKeys = append(envKeys, p.envVar)
			backends = append(backends, initBackend{
				name:     "cloud-" + p.name,
				url:      p.url,
				typ:      p.typ,
				model:    p.model,
				apiKey:   "env:" + p.envVar,
				costIn:   p.costIn,
				costOut:  p.costOut,
				caps:     []string{"json_output", "long_context"},
				maxConc:  50,
				priority: 50,
			})
		}
	}

	fmt.Println()

	if len(backends) == 0 {
		fmt.Println("No backends detected. Start Ollama or set an API key:")
		fmt.Println("  ollama serve                    # start Ollama")
		fmt.Println("  export GEMINI_API_KEY=...       # or any cloud provider")
		fmt.Println("  export OPENAI_API_KEY=...")
		fmt.Println()
		fmt.Println("Then re-run: kronaxis-router init")
		os.Exit(1)
	}

	// Generate config
	yamlContent := generateInitConfig(backends, *flagPort)
	if err := os.WriteFile(*flagOutput, []byte(yamlContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Config written to %s (%d backends, %d rules)\n", *flagOutput, len(backends), countRules(backends))
	fmt.Println()

	// Print tool-specific instructions
	routerURL := fmt.Sprintf("http://localhost:%d", *flagPort)

	switch {
	case *flagAider:
		printAiderInstructions(routerURL)
	case *flagContinue:
		printContinueInstructions(routerURL)
	case *flagOpenWebUI:
		printOpenWebUIInstructions(routerURL)
	case *flagCursor:
		printCursorInstructions()
	case *flagClaude:
		configureClaude()
	default:
		printGenericInstructions(routerURL, *flagPort)
	}
}

type initBackend struct {
	name     string
	url      string
	typ      string
	model    string
	apiKey   string
	costIn   float64
	costOut  float64
	caps     []string
	maxConc  int
	priority int // higher = more capable
}

func detectOllama() []string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	var names []string
	for _, m := range result.Models {
		names = append(names, m.Name)
	}
	return names
}

func detectVLLM(url string) []string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url + "/v1/models")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	var names []string
	for _, m := range result.Data {
		names = append(names, m.ID)
	}
	return names
}

func sanitiseName(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ToLower(s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// priorityForModel assigns a rough capability priority based on model name patterns.
func priorityForModel(name string) int {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "70b") || strings.Contains(lower, "72b"):
		return 90
	case strings.Contains(lower, "27b") || strings.Contains(lower, "32b") || strings.Contains(lower, "34b"):
		return 70
	case strings.Contains(lower, "14b") || strings.Contains(lower, "13b"):
		return 50
	case strings.Contains(lower, "7b") || strings.Contains(lower, "8b") || strings.Contains(lower, "9b"):
		return 30
	case strings.Contains(lower, "3b") || strings.Contains(lower, "4b") || strings.Contains(lower, "1b"):
		return 10
	default:
		return 40
	}
}

func countRules(backends []initBackend) int {
	// default + tier1 + tier2 + bulk = 4 base rules
	count := 4
	if hasLocal(backends) && hasCloud(backends) {
		count++ // local-first rule
	}
	return count
}

func hasLocal(backends []initBackend) bool {
	for _, b := range backends {
		if b.typ == "ollama" || b.typ == "vllm" {
			return true
		}
	}
	return false
}

func hasCloud(backends []initBackend) bool {
	for _, b := range backends {
		if b.typ == "gemini" || b.typ == "openai" {
			return true
		}
	}
	return false
}

func generateInitConfig(backends []initBackend, port int) string {
	var sb strings.Builder

	sb.WriteString("# Kronaxis Router - Auto-generated by 'kronaxis-router init'\n")
	sb.WriteString("# Edit as needed. Config hot-reloads every 5 seconds.\n")
	sb.WriteString("# Docs: https://github.com/Kronaxis/kronaxis-router\n\n")

	// Server
	sb.WriteString(fmt.Sprintf("server:\n  port: %d\n  health_check_interval: 30s\n  default_timeout: 120s\n  branding:\n    headers: true\n    header_name: \"Kronaxis Router\"\n\n", port))

	// Backends
	sb.WriteString("backends:\n")
	for _, b := range backends {
		sb.WriteString(fmt.Sprintf("  - name: %s\n", b.name))
		sb.WriteString(fmt.Sprintf("    url: \"%s\"\n", b.url))
		sb.WriteString(fmt.Sprintf("    type: %s\n", b.typ))
		sb.WriteString(fmt.Sprintf("    model_name: \"%s\"\n", b.model))
		if b.apiKey != "" {
			sb.WriteString(fmt.Sprintf("    api_key: \"%s\"\n", b.apiKey))
		}
		sb.WriteString(fmt.Sprintf("    cost_input_1m: %.2f\n", b.costIn))
		sb.WriteString(fmt.Sprintf("    cost_output_1m: %.2f\n", b.costOut))
		sb.WriteString(fmt.Sprintf("    capabilities: [%s]\n", strings.Join(b.caps, ", ")))
		sb.WriteString(fmt.Sprintf("    max_concurrent: %d\n", b.maxConc))
		sb.WriteString("\n")
	}

	// Sort backends by cost for rule generation
	var cheapest, mostCapable string
	var cheapestCost float64 = 999999
	var highestPriority int
	var allNames []string

	for _, b := range backends {
		allNames = append(allNames, b.name)
		totalCost := b.costIn + b.costOut
		if totalCost < cheapestCost {
			cheapestCost = totalCost
			cheapest = b.name
		}
		if b.priority > highestPriority {
			highestPriority = b.priority
			mostCapable = b.name
		}
	}

	// Build fallback chain: most capable first, then others
	var fallback []string
	fallback = append(fallback, mostCapable)
	for _, n := range allNames {
		if n != mostCapable {
			fallback = append(fallback, n)
		}
	}

	// Build cheap chain: cheapest first, then others
	var cheapChain []string
	cheapChain = append(cheapChain, cheapest)
	for _, n := range allNames {
		if n != cheapest {
			cheapChain = append(cheapChain, n)
		}
	}

	// Rules
	sb.WriteString("rules:\n")

	// Rule 1: Heavy reasoning -> most capable
	sb.WriteString("  - name: heavy-reasoning\n")
	sb.WriteString("    priority: 200\n")
	sb.WriteString("    match:\n      tier: 1\n")
	sb.WriteString(fmt.Sprintf("    backends: [%s]\n\n", strings.Join(fallback, ", ")))

	// Rule 2: Light extraction -> cheapest
	sb.WriteString("  - name: cheap-extraction\n")
	sb.WriteString("    priority: 150\n")
	sb.WriteString("    match:\n      tier: 2\n")
	sb.WriteString(fmt.Sprintf("    backends: [%s]\n\n", strings.Join(cheapChain, ", ")))

	// Rule 3: Bulk -> cheapest with batch
	sb.WriteString("  - name: bulk-work\n")
	sb.WriteString("    priority: 180\n")
	sb.WriteString("    match:\n      priority_level: bulk\n")
	sb.WriteString(fmt.Sprintf("    backends: [%s]\n\n", strings.Join(cheapChain, ", ")))

	// Rule 4: Interactive -> fastest (most capable, skips batching)
	sb.WriteString("  - name: interactive\n")
	sb.WriteString("    priority: 190\n")
	sb.WriteString("    match:\n      priority_level: interactive\n")
	sb.WriteString(fmt.Sprintf("    backends: [%s]\n\n", strings.Join(fallback, ", ")))

	// Rule 5: Default catch-all
	sb.WriteString("  - name: default\n")
	sb.WriteString("    priority: 100\n")
	sb.WriteString("    match: {}\n")
	sb.WriteString(fmt.Sprintf("    backends: [%s]\n\n", strings.Join(fallback, ", ")))

	// Defaults
	sb.WriteString("defaults:\n")
	sb.WriteString(fmt.Sprintf("  fallback_chain: [%s]\n", strings.Join(fallback, ", ")))
	sb.WriteString("  default_timeout_ms: 120000\n\n")

	// Budgets
	sb.WriteString("budgets:\n")
	sb.WriteString("  default:\n")
	if hasCloud(backends) {
		sb.WriteString("    daily_limit_usd: 10.00\n")
	} else {
		sb.WriteString("    daily_limit_usd: 1.00\n")
	}
	sb.WriteString("    action: downgrade\n")
	sb.WriteString(fmt.Sprintf("    downgrade_target: %s\n\n", cheapest))

	// Rate limits
	sb.WriteString("rate_limits:\n")
	sb.WriteString("  default:\n")
	sb.WriteString("    requests_per_second: 100\n")
	sb.WriteString("    burst_size: 200\n\n")

	// Batching
	sb.WriteString("batching:\n")
	sb.WriteString("  enabled: true\n")
	sb.WriteString("  window_ms: 50\n")
	sb.WriteString("  max_batch_size: 8\n")
	sb.WriteString("  priority_bypass: [interactive]\n")

	return sb.String()
}

// Tool-specific integration instructions

func printGenericInstructions(routerURL string, _ int) {
	fmt.Printf("Start the router:\n")
	fmt.Printf("  kronaxis-router\n\n")
	fmt.Printf("Point your LLM client at:\n")
	fmt.Printf("  %s/v1/chat/completions\n\n", routerURL)
	fmt.Printf("Dashboard:\n")
	fmt.Printf("  %s\n\n", routerURL)
	fmt.Printf("Tool-specific setup:\n")
	fmt.Printf("  kronaxis-router init --aider      # Aider\n")
	fmt.Printf("  kronaxis-router init --continue    # Continue.dev\n")
	fmt.Printf("  kronaxis-router init --cursor      # Cursor\n")
	fmt.Printf("  kronaxis-router init --claude       # Claude Code\n")
	fmt.Printf("  kronaxis-router init --openwebui   # Open WebUI\n")
}

func printAiderInstructions(routerURL string) {
	fmt.Println("=== Aider Integration ===")
	fmt.Println()
	fmt.Println("Option 1: Environment variables")
	fmt.Printf("  export OPENAI_API_BASE=%s/v1\n", routerURL)
	fmt.Println("  export OPENAI_API_KEY=not-needed  # router handles auth")
	fmt.Println("  aider --model openai/default")
	fmt.Println()
	fmt.Println("Option 2: .aider.conf.yml")
	fmt.Printf("  openai-api-base: %s/v1\n", routerURL)
	fmt.Println("  openai-api-key: not-needed")
	fmt.Println("  model: openai/default")
	fmt.Println()
	fmt.Println("Start the router first, then run aider as usual.")
}

func printContinueInstructions(routerURL string) {
	fmt.Println("=== Continue.dev Integration ===")
	fmt.Println()
	fmt.Println("Add to ~/.continue/config.json:")
	fmt.Println()
	fmt.Printf(`  {
    "models": [
      {
        "title": "Kronaxis Router",
        "provider": "openai",
        "model": "default",
        "apiBase": "%s/v1",
        "apiKey": "not-needed"
      }
    ]
  }
`, routerURL)
	fmt.Println()
	fmt.Println("Start the router first, then open Continue in VS Code.")
}

func printOpenWebUIInstructions(routerURL string) {
	fmt.Println("=== Open WebUI Integration ===")
	fmt.Println()
	fmt.Println("In Open WebUI Settings > Connections:")
	fmt.Printf("  OpenAI API Base URL: %s/v1\n", routerURL)
	fmt.Println("  API Key: not-needed")
	fmt.Println()
	fmt.Println("The router's backends will appear as available models.")
}

func printCursorInstructions() {
	fmt.Println("=== Cursor MCP Integration ===")
	fmt.Println()
	fmt.Println("Add to your project's .cursor/mcp.json:")
	fmt.Println()
	fmt.Println(`  {
    "mcpServers": {
      "kronaxis-router": {
        "command": "kronaxis-router",
        "args": ["mcp"],
        "env": {
          "ROUTER_URL": "http://localhost:8050"
        }
      }
    }
  }`)
	fmt.Println()
	fmt.Println("This gives Cursor tools to manage backends, costs, and routing rules.")
}

func configureClaude() {
	fmt.Println("=== Claude Code MCP Integration ===")
	fmt.Println()

	// Try to find Claude Code settings
	home, err := os.UserHomeDir()
	if err != nil {
		printClaudeManualInstructions()
		return
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Read existing settings or create new
	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings == nil {
		settings = make(map[string]interface{})
	}

	// Add MCP server config
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		mcpServers = make(map[string]interface{})
	}
	mcpServers["kronaxis-router"] = map[string]interface{}{
		"command": "kronaxis-router",
		"args":    []string{"mcp"},
		"env": map[string]string{
			"ROUTER_URL": "http://localhost:8050",
		},
	}
	settings["mcpServers"] = mcpServers

	// Write back
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Printf("Error serialising settings: %v\n", err)
		printClaudeManualInstructions()
		return
	}

	// Ensure directory exists
	os.MkdirAll(filepath.Join(home, ".claude"), 0755)

	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		fmt.Printf("Error writing %s: %v\n", settingsPath, err)
		printClaudeManualInstructions()
		return
	}

	fmt.Printf("MCP server added to %s\n", settingsPath)
	fmt.Println()
	fmt.Println("Claude Code now has these tools:")
	fmt.Println("  router_health      - check backend status")
	fmt.Println("  router_backends    - list/add/remove backends")
	fmt.Println("  router_costs       - view daily spending")
	fmt.Println("  router_rules       - manage routing rules")
	fmt.Println("  router_stats       - live request metrics")
	fmt.Println()
	fmt.Println("Start the router, then restart Claude Code.")
}

func printClaudeManualInstructions() {
	fmt.Println("Add to ~/.claude/settings.json:")
	fmt.Println()
	fmt.Println(`  {
    "mcpServers": {
      "kronaxis-router": {
        "command": "kronaxis-router",
        "args": ["mcp"],
        "env": {
          "ROUTER_URL": "http://localhost:8050"
        }
      }
    }
  }`)
	fmt.Println()
	fmt.Println("Then restart Claude Code.")
}
