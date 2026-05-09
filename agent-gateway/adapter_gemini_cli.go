package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// GeminiCLIAdapter spawns the `gemini` CLI in non-interactive mode and streams
// stdout as text deltas. The Gemini CLI's headless surface is leaner than
// Claude Code's: no skills, no MCP, but it does support file editing in cwd
// and tool use. We treat its stdout as plain text streaming for v1.
//
// Available() returns true iff the configured `gemini` binary is on PATH.
type GeminiCLIAdapter struct {
	Binary string
}

func (a *GeminiCLIAdapter) Name() string { return "gemini-cli" }

func (a *GeminiCLIAdapter) Available() bool {
	if a.Binary == "" {
		return false
	}
	_, err := exec.LookPath(a.Binary)
	return err == nil
}

func (a *GeminiCLIAdapter) Capabilities() Capabilities {
	return Capabilities{
		Tools:          true,
		FileEdit:       true,
		Streaming:      true,
		MaxContext:     1_000_000,
		SkillsCommands: false,
	}
}

func (a *GeminiCLIAdapter) Run(ctx context.Context, req AgentRequest, events chan<- AgentEvent) error {
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

	args := []string{"--prompt", prompt}
	if req.ClaudeModel != "" {
		args = append(args, "--model", req.ClaudeModel)
	}
	cmd := exec.CommandContext(runCtx, a.Binary, args...)
	cmd.Dir = req.WorkspacePath

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start gemini: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	streamErr := streamGeminiStdout(stdout, events)
	waitErr := cmd.Wait()
	if waitErr != nil && streamErr == nil {
		select {
		case events <- AgentEvent{Type: EventError, Error: waitErr.Error()}:
		default:
		}
	}
	events <- AgentEvent{Type: EventDone, StopReason: "end_turn", NumTurns: 1}
	return streamErr
}

func streamGeminiStdout(r io.Reader, events chan<- AgentEvent) error {
	reader := bufio.NewReader(r)
	for {
		chunk, err := reader.ReadString('\n')
		if chunk != "" {
			events <- AgentEvent{Type: EventTextDelta, Content: chunk}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read stdout: %w", err)
		}
	}
}
