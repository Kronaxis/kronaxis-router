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
  test    <name>      send a smoke prompt through the agent

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
	timeout := fs.Duration("timeout", 30*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "test: agent name required")
		os.Exit(2)
	}
	model := fs.Arg(0)
	body := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": *prompt}},
		"stream":   false,
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", strings.TrimRight(*gateway, "/")+"/v1/chat/completions", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Printf("status: %d\n", resp.StatusCode)
	fmt.Println(string(out))
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
