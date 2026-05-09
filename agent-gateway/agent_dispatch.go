package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kronaxis/agent-gateway/accounts"
	regpkg "github.com/kronaxis/agent-gateway/registry"
	"github.com/kronaxis/agent-gateway/runner"
	"github.com/kronaxis/agent-gateway/workspace"
)

// dispatchProfileAgent runs a profile-driven CLI agent and forwards events
// onto the existing AgentEvent channel. Returns (true, err) if the model
// matched a registered profile; (false, nil) if the caller should fall
// through to the legacy adapter registry.
func (s *Server) dispatchProfileAgent(ctx context.Context, model string, req AgentRequest, events chan<- AgentEvent) (handled bool, err error) {
	if s.profileReg == nil {
		return false, nil
	}
	agentName, submodel := splitAgentSubmodel(model)
	prof, ok := s.profileReg.Get(agentName)
	if !ok {
		return false, nil
	}

	// Submodel validation per profile.
	if submodel != "" && !prof.Submodel.Supports {
		return true, fmt.Errorf("agent %q does not support submodels", prof.Name)
	}
	if submodel != "" && len(prof.Submodel.Allowed) > 0 {
		ok := false
		for _, a := range prof.Submodel.Allowed {
			if a == submodel {
				ok = true
				break
			}
		}
		if !ok {
			return true, fmt.Errorf("submodel %q not allowed for %q (allowed: %v)", submodel, prof.Name, prof.Submodel.Allowed)
		}
	}
	if submodel == "" {
		submodel = prof.Submodel.Default
	}

	// Account checkout.
	var lease *accounts.Lease
	if s.accountMgr != nil {
		l, err := s.accountMgr.Checkout(prof.Auth.Pool)
		if err != nil {
			if errors.Is(err, accounts.ErrPoolExhausted) {
				return true, fmt.Errorf("pool %q exhausted; retry after cooldown", prof.Auth.Pool)
			}
			if errors.Is(err, accounts.ErrUnknownPool) {
				return true, fmt.Errorf("pool %q not configured (add accounts to accounts.yaml)", prof.Auth.Pool)
			}
			return true, err
		}
		lease = l
	}

	// Workspace per profile.
	wsType := workspace.Type(prof.Workspace.Type)
	auxDirs := []string{}
	if prof.Auth.Injection == regpkg.AuthInjectConfigDir {
		auxDirs = append(auxDirs, "config-dir")
	}
	ws, err := workspace.New(workspace.Spec{
		Type:         wsType,
		Root:         s.cfg.WorkspaceRoot,
		RequestID:    req.RequestID,
		AuxDirs:      auxDirs,
		GitInit:      wsType == workspace.TypeWorktreeEphemeral,
		GitUserEmail: "agent@kronaxis.local",
		GitUserName:  "agent",
	})
	if err != nil {
		if lease != nil {
			lease.Release(accounts.OutcomeTransient, err.Error())
		}
		return true, fmt.Errorf("workspace: %w", err)
	}
	if err := ws.Setup(ctx); err != nil {
		if lease != nil {
			lease.Release(accounts.OutcomeTransient, err.Error())
		}
		return true, fmt.Errorf("workspace setup: %w", err)
	}
	defer func() { _ = ws.Cleanup() }()

	parser, err := runner.LookupParser(prof.Output.Parser)
	if err != nil {
		if lease != nil {
			lease.Release(accounts.OutcomeTransient, err.Error())
		}
		return true, err
	}

	// Convert ChatMessage -> runner.Message.
	msgs := make([]runner.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, runner.Message{Role: m.Role, Content: m.Content})
	}

	rreq := runner.Request{
		Profile:           prof,
		Submodel:          submodel,
		Messages:          msgs,
		SystemPrompt:      req.SystemPrompt,
		AppendSystem:      req.AppendSystemPrompt,
		Workspace:         ws,
		Lease:             lease,
		TimeoutSec:        req.TimeoutSec,
		Agent:             req.Agent,
		PermissionMode:    req.PermissionMode,
		Effort:            req.Effort,
		AllowedTools:      req.AllowedTools,
		DisallowedTools:   req.DisallowedTools,
		MCPConfig:         req.MCPConfig,
		AddDirs:           req.AddDirs,
		Bare:              req.Bare,
		IncludeHookEvents: req.IncludeHookEvents,
	}

	// Bridge runner events onto AgentEvent channel.
	rch := make(chan runner.Event, 32)
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		for ev := range rch {
			ae := convertEvent(ev)
			select {
			case events <- ae:
			case <-ctx.Done():
				return
			}
		}
	}()

	_, runErr := runner.Run(ctx, rreq, parser, rch)
	close(rch)
	<-bridgeDone
	return true, runErr
}

func convertEvent(ev runner.Event) AgentEvent {
	out := AgentEvent{
		Type:         EventType(ev.Type),
		Content:      ev.Content,
		Error:        ev.Error,
		StopReason:   ev.StopReason,
		NumTurns:     ev.NumTurns,
		CostUSD:      ev.CostUSD,
		InputTokens:  ev.InputTokens,
		OutputTokens: ev.OutputTokens,
	}
	if ev.Tool != nil {
		out.Tool = &ToolEvent{
			Index:       ev.Tool.Index,
			Name:        ev.Tool.Name,
			ID:          ev.Tool.ID,
			PartialJSON: ev.Tool.PartialJSON,
			FinalInput:  ev.Tool.FinalInput,
		}
	}
	return out
}

// splitAgentSubmodel parses "<agent>/<submodel>" → (agent, submodel).
// Returns (model, "") if no slash. Strips any trailing "+skill" suffix
// from the agent half (skills are unrelated to submodels).
func splitAgentSubmodel(model string) (string, string) {
	base, _ := splitModelSkill(model)
	idx := strings.IndexByte(base, '/')
	if idx < 0 {
		return base, ""
	}
	return base[:idx], base[idx+1:]
}
