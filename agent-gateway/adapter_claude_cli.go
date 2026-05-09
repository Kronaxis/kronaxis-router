package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type ClaudeCLIAdapter struct {
	Binary string
}

func (a *ClaudeCLIAdapter) Name() string    { return "claude-cli" }
func (a *ClaudeCLIAdapter) Available() bool { return true }

func (a *ClaudeCLIAdapter) Capabilities() Capabilities {
	return Capabilities{
		Tools:          true,
		FileEdit:       true,
		Streaming:      true,
		MaxContext:     1_000_000,
		SkillsCommands: true,
	}
}

type claudeStreamLine struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype,omitempty"`
	Event          *claudeInner    `json:"event,omitempty"`
	Result         string          `json:"result,omitempty"`
	NumTurns       int             `json:"num_turns,omitempty"`
	StopReason     string          `json:"stop_reason,omitempty"`
	TotalCostUSD   float64         `json:"total_cost_usd,omitempty"`
	Usage          *claudeUsage    `json:"usage,omitempty"`
	IsError        bool            `json:"is_error,omitempty"`
	APIErrorStatus json.RawMessage `json:"api_error_status,omitempty"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type claudeInner struct {
	Type         string       `json:"type"`
	Index        int          `json:"index,omitempty"`
	ContentBlock *claudeBlock `json:"content_block,omitempty"`
	Delta        *claudeDelta `json:"delta,omitempty"`
}

type claudeBlock struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type claudeDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
}

func (a *ClaudeCLIAdapter) Run(ctx context.Context, req AgentRequest, events chan<- AgentEvent) error {
	prompt := flattenMessages(req.Messages)
	if prompt == "" {
		return fmt.Errorf("empty prompt")
	}

	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := buildClaudeArgs(req, prompt)
	cmd := exec.CommandContext(runCtx, a.Binary, args...)
	cmd.Dir = req.WorkspacePath
	env := append(envCleanForClaude(), "CLAUDE_CONFIG_DIR="+req.ClaudeConfig)
	if req.APIKey != "" {
		env = append(env, "ANTHROPIC_API_KEY="+req.APIKey)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	parseErr := parseClaudeStream(stdout, events)

	waitErr := cmd.Wait()
	if waitErr != nil && parseErr == nil {
		select {
		case events <- AgentEvent{Type: EventError, Error: waitErr.Error()}:
		default:
		}
	}
	return parseErr
}

func buildClaudeArgs(req AgentRequest, prompt string) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--no-session-persistence",
		"--verbose",
	}
	if req.Bare {
		args = append(args, "--bare")
	}
	permMode := req.PermissionMode
	if permMode == "" {
		permMode = "bypassPermissions"
	}
	args = append(args, "--permission-mode", permMode)

	if req.SystemPrompt != "" {
		args = append(args, "--system-prompt", req.SystemPrompt)
	}
	if req.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", req.AppendSystemPrompt)
	}
	if req.Agent != "" {
		args = append(args, "--agent", req.Agent)
	}
	if req.ClaudeModel != "" {
		args = append(args, "--model", req.ClaudeModel)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.MCPConfig != "" {
		args = append(args, "--mcp-config", req.MCPConfig)
	}
	for _, t := range req.AllowedTools {
		args = append(args, "--allowedTools", t)
	}
	for _, t := range req.DisallowedTools {
		args = append(args, "--disallowedTools", t)
	}
	for _, d := range req.AddDirs {
		args = append(args, "--add-dir", d)
	}
	if req.IncludeHookEvents {
		args = append(args, "--include-hook-events")
	}
	args = append(args, prompt)
	return args
}

func parseClaudeStream(r io.Reader, events chan<- AgentEvent) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	type toolBlock struct {
		openAIIdx int
		name      string
		id        string
	}
	currentBlocks := map[int]*toolBlock{}
	nextOpenAIIdx := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev claudeStreamLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "stream_event":
			if ev.Event == nil {
				continue
			}
			switch ev.Event.Type {
			case "content_block_start":
				if ev.Event.ContentBlock != nil && ev.Event.ContentBlock.Type == "tool_use" {
					tb := &toolBlock{
						openAIIdx: nextOpenAIIdx,
						name:      ev.Event.ContentBlock.Name,
						id:        ev.Event.ContentBlock.ID,
					}
					nextOpenAIIdx++
					currentBlocks[ev.Event.Index] = tb
					events <- AgentEvent{
						Type: EventToolCallStart,
						Tool: &ToolEvent{
							Index: tb.openAIIdx,
							Name:  tb.name,
							ID:    tb.id,
						},
					}
				}
			case "content_block_delta":
				if ev.Event.Delta == nil {
					continue
				}
				switch ev.Event.Delta.Type {
				case "text_delta":
					if ev.Event.Delta.Text != "" {
						events <- AgentEvent{Type: EventTextDelta, Content: ev.Event.Delta.Text}
					}
				case "input_json_delta":
					tb, ok := currentBlocks[ev.Event.Index]
					if !ok {
						continue
					}
					events <- AgentEvent{
						Type: EventToolCallDelta,
						Tool: &ToolEvent{
							Index:       tb.openAIIdx,
							PartialJSON: ev.Event.Delta.PartialJSON,
						},
					}
				}
			case "content_block_stop":
				if tb, ok := currentBlocks[ev.Event.Index]; ok {
					events <- AgentEvent{
						Type: EventToolCallEnd,
						Tool: &ToolEvent{Index: tb.openAIIdx, Name: tb.name},
					}
					delete(currentBlocks, ev.Event.Index)
				}
			}
		case "result":
			done := AgentEvent{
				Type:       EventDone,
				StopReason: ev.StopReason,
				NumTurns:   ev.NumTurns,
				CostUSD:    ev.TotalCostUSD,
			}
			if ev.Usage != nil {
				done.InputTokens = ev.Usage.InputTokens
				done.OutputTokens = ev.Usage.OutputTokens
			}
			events <- done
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan stream: %w", err)
	}
	events <- AgentEvent{Type: EventDone, StopReason: "stream_closed"}
	return nil
}

func flattenMessages(msgs []ChatMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	if len(msgs) == 1 {
		return msgs[0].Content
	}
	var b strings.Builder
	for _, m := range msgs {
		role := strings.ToUpper(m.Role)
		fmt.Fprintf(&b, "%s: %s\n\n", role, m.Content)
	}
	return strings.TrimSpace(b.String())
}

func envCleanForClaude() []string {
	const stripPrefix = "CLAUDE_CODE_"
	out := []string{}
	for _, kv := range osEnviron() {
		if strings.HasPrefix(kv, stripPrefix) || strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
