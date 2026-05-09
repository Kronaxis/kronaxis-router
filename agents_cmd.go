package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// runAgentsCmd handles `kronaxis-router agents <subcommand>`.
//
//	register <name>           — fetch profile from gateway, write backend +
//	                            tier-rule into config.yaml
//	list                      — print registered agents and their tier slot
//	remove <name>             — strip backend stanza + rule references
//	test    <name>            — send a tiny prompt through the gateway and
//	                            print the result (smoke-test)
func runAgentsCmd(args []string) {
	if len(args) == 0 {
		printAgentsUsage()
		os.Exit(2)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "register":
		runAgentsRegister(rest)
	case "list":
		runAgentsList(rest)
	case "remove", "delete", "rm":
		runAgentsRemove(rest)
	case "test":
		runAgentsTest(rest)
	case "sync":
		runAgentsSync(rest)
	case "help", "-h", "--help":
		printAgentsUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown agents subcommand: %q\n", sub)
		printAgentsUsage()
		os.Exit(2)
	}
}

func printAgentsUsage() {
	fmt.Fprintln(os.Stderr, `usage: kronaxis-router agents <subcommand> [flags]

subcommands:
  register <name>     register a CLI agent as a routable backend
  list                list registered agents
  remove  <name>      remove a registered agent
  test    <name>      send a smoke prompt through the agent (end-to-end)
  sync                bulk-register every profile the gateway exposes

global flags:
  --gateway URL       agent-gateway base URL (default http://localhost:8055)
  --config  PATH      router config.yaml path (default $CONFIG_PATH or ./config.yaml)`)
}

func runAgentsRegister(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	gateway := fs.String("gateway", "http://localhost:8055", "agent-gateway base URL")
	configPath := fs.String("config", AgentConfigPath(), "router config.yaml")
	tier := fs.Int("tier", 0, "override tier (default: from profile.routing_default.tier)")
	costClass := fs.String("cost-class", "", "override cost_class (default: from profile)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "register: agent name required")
		os.Exit(2)
	}
	name := fs.Arg(0)

	prof, err := fetchProfile(*gateway, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch profile %s: %v\n", name, err)
		os.Exit(1)
	}
	in := AgentSynthInput{
		Name:         prof.Name,
		GatewayURL:   *gateway,
		Model:        prof.Name,
		Tier:         prof.RoutingDefault.Tier,
		CostClass:    string(prof.RoutingDefault.CostClass),
		Capabilities: prof.Capabilities,
	}
	if *tier > 0 {
		in.Tier = *tier
	}
	if *costClass != "" {
		in.CostClass = *costClass
	}
	if err := SynthAgentBackend(*configPath, in); err != nil {
		fmt.Fprintf(os.Stderr, "synth: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("registered %s -> %s (tier %d, %s) in %s\n", in.Name, in.GatewayURL, in.Tier, in.CostClass, *configPath)
}

func runAgentsList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	gateway := fs.String("gateway", "http://localhost:8055", "agent-gateway base URL")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	profs, err := fetchProfileList(*gateway)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(profs, func(i, j int) bool { return profs[i].Name < profs[j].Name })
	w := io.Writer(os.Stdout)
	fmt.Fprintf(w, "%-20s  %-12s  %-6s  %-12s  %s\n", "NAME", "TIER", "WS", "AUTH POOL", "DISPLAY")
	for _, p := range profs {
		fmt.Fprintf(w, "%-20s  %-12s  %-6s  %-12s  %s\n",
			p.Name, p.Tier, abbrevWS(p.Workspace.Type), p.Auth.Pool, p.DisplayName)
	}
}

func runAgentsRemove(args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	configPath := fs.String("config", AgentConfigPath(), "router config.yaml")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "remove: agent name required")
		os.Exit(2)
	}
	if err := RemoveAgentBackend(*configPath, fs.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "remove: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("removed %s from %s\n", fs.Arg(0), *configPath)
}

func runAgentsTest(args []string) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	gateway := fs.String("gateway", "http://localhost:8055", "agent-gateway base URL")
	prompt := fs.String("prompt", "Reply with the literal word READY and nothing else.", "smoke prompt")
	timeout := fs.Duration("timeout", 60*time.Second, "request timeout")
	verbose := fs.Bool("v", false, "verbose: print full response JSON")
	viaRouter := fs.String("via-router", "", "if set, send through this router base URL instead of the gateway directly")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "test: agent name required")
		os.Exit(2)
	}
	model := fs.Arg(0)

	// Pre-flight: confirm the agent is registered on the gateway.
	if _, err := fetchProfile(*gateway, agentBaseName(model)); err != nil {
		fmt.Fprintf(os.Stderr, "agent %q not on gateway %s: %v\n", agentBaseName(model), *gateway, err)
		os.Exit(1)
	}

	target := strings.TrimRight(*gateway, "/") + "/v1/chat/completions"
	if *viaRouter != "" {
		target = strings.TrimRight(*viaRouter, "/") + "/v1/chat/completions"
	}

	body := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": *prompt}},
		"stream":   false,
	}
	raw, _ := json.Marshal(body)
	httpReq, _ := http.NewRequest("POST", target, strings.NewReader(string(raw)))
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(start)

	fmt.Printf("model:       %s\n", model)
	fmt.Printf("target:      %s\n", target)
	fmt.Printf("http status: %d\n", resp.StatusCode)
	fmt.Printf("elapsed:     %s\n", elapsed.Round(time.Millisecond))

	// Parse OpenAI chat-completion shape and report verdict.
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Error any `json:"error,omitempty"`
	}
	if err := json.Unmarshal(out, &chat); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\nbody: %s\n", err, string(out))
		os.Exit(1)
	}
	if chat.Error != nil {
		fmt.Printf("verdict:     FAIL (error returned: %v)\n", chat.Error)
		os.Exit(1)
	}
	if len(chat.Choices) == 0 {
		fmt.Printf("verdict:     FAIL (no choices in response)\n")
		os.Exit(1)
	}
	content := strings.TrimSpace(chat.Choices[0].Message.Content)
	fmt.Printf("tokens:      %d in / %d out\n", chat.Usage.PromptTokens, chat.Usage.CompletionTokens)
	if *verbose {
		fmt.Println("--- response ---")
		fmt.Println(string(out))
	}
	fmt.Printf("response:    %s\n", truncate(content, 240))
	if resp.StatusCode == 200 && content != "" {
		fmt.Println("verdict:     OK")
		return
	}
	fmt.Println("verdict:     FAIL (empty content or non-200 status)")
	os.Exit(1)
}

func runAgentsSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	gateway := fs.String("gateway", "http://localhost:8055", "agent-gateway base URL")
	configPath := fs.String("config", AgentConfigPath(), "router config.yaml")
	dryRun := fs.Bool("dry-run", false, "print what would be added; do not write")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	profs, err := fetchProfileList(*gateway)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync: list profiles: %v\n", err)
		os.Exit(1)
	}
	added := 0
	skipped := 0
	for _, p := range profs {
		in := AgentSynthInput{
			Name:         p.Name,
			GatewayURL:   *gateway,
			Model:        p.Name,
			Tier:         p.RoutingDefault.Tier,
			CostClass:    p.RoutingDefault.CostClass,
			Capabilities: p.Capabilities,
		}
		if *dryRun {
			fmt.Printf("[dry-run] would register %-20s tier=%d cost=%-8s caps=%v\n",
				in.Name, in.Tier, in.CostClass, in.Capabilities)
			continue
		}
		if err := SynthAgentBackend(*configPath, in); err != nil {
			fmt.Fprintf(os.Stderr, "sync %s: %v\n", p.Name, err)
			skipped++
			continue
		}
		fmt.Printf("registered %-20s tier=%d cost=%s\n", in.Name, in.Tier, in.CostClass)
		added++
	}
	if *dryRun {
		fmt.Printf("dry-run: %d profiles\n", len(profs))
		return
	}
	fmt.Printf("done: %d added, %d skipped\n", added, skipped)
}

func agentBaseName(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		return model[:i]
	}
	return model
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// fetchProfile retrieves a profile from the agent-gateway's /v1/agents/<name>.
// Returns a minimal local copy with the fields synth needs. Uses untyped JSON
// to avoid an import cycle / cross-module dep on the agent-gateway types.
func fetchProfile(gateway, name string) (*profileLite, error) {
	u, err := url.Parse(strings.TrimRight(gateway, "/") + "/v1/agents/" + name)
	if err != nil {
		return nil, err
	}
	resp, err := http.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var p profileLite
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func fetchProfileList(gateway string) ([]profileLite, error) {
	resp, err := http.Get(strings.TrimRight(gateway, "/") + "/v1/agents")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var env struct {
		Data []profileLite `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// profileLite mirrors the JSON-level fields we read from the gateway. It is
// NOT a full mirror of the gateway's registry.Profile struct.
type profileLite struct {
	Name           string   `json:"name"`
	DisplayName    string   `json:"display_name,omitempty"`
	Tier           string   `json:"tier"`
	Capabilities   []string `json:"capabilities,omitempty"`
	Workspace      struct {
		Type string `json:"type"`
	} `json:"workspace"`
	Auth struct {
		Pool string `json:"pool"`
	} `json:"auth"`
	Submodel struct {
		Supports bool     `json:"supports"`
		Default  string   `json:"default,omitempty"`
		Allowed  []string `json:"allowed,omitempty"`
	} `json:"submodel,omitempty"`
	RoutingDefault struct {
		Tier      int    `json:"tier"`
		CostClass string `json:"cost_class,omitempty"`
	} `json:"routing_default,omitempty"`
}

func abbrevWS(t string) string {
	switch t {
	case "worktree-ephemeral":
		return "wt"
	case "dir-ephemeral":
		return "dir"
	case "stateless":
		return "none"
	}
	return t
}
