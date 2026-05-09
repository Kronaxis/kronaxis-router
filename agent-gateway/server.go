package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Server struct {
	cfg      *Config
	registry *Registry
	sem      chan struct{}
	logger   *log.Logger
	audit    AuditLogger
	pool     *WarmPool
	metrics  *Metrics
	wsStore  *WorkspaceStore
	liveBus  *liveBus
	auth     *AuthPool
}

func newServer(cfg *Config, reg *Registry, logger *log.Logger, audit AuditLogger, pool *WarmPool, metrics *Metrics, bus *liveBus, auth *AuthPool) *Server {
	return &Server{
		cfg:      cfg,
		registry: reg,
		sem:      make(chan struct{}, cfg.MaxConcurrent),
		logger:   logger,
		audit:    audit,
		pool:     pool,
		metrics:  metrics,
		wsStore:  newWorkspaceStore(),
		liveBus:  bus,
		auth:     auth,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/workspaces", s.handleWorkspaces)
	mux.HandleFunc("/v1/workspaces/", s.handleWorkspaceItem)
	mux.HandleFunc("/v1/accounts", s.handleAccounts)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.Handle("/metrics", s.metrics.Handler())
	mux.HandleFunc("/api/live", s.handleLive)
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

func (s *Server) handleAccounts(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   s.auth.Snapshot(),
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if _, err := exec.LookPath(s.cfg.ClaudeBinary); err != nil {
		http.Error(w, "claude binary not found: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := ModelsResponse{Object: "list", Data: s.registry.Models()}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages required", http.StatusBadRequest)
		return
	}

	requestID := newRequestID()
	startTime := time.Now()
	rec := RequestRecord{
		ID:         requestID,
		Model:      req.Model,
		Stream:     req.Stream,
		UserAgent:  r.UserAgent(),
		RemoteAddr: r.RemoteAddr,
	}

	// Parse "model+skill" suffix and apply skill prefix
	baseModel, skillFromModel := splitModelSkill(req.Model)
	req.Model = baseModel
	if skillFromModel != "" && req.Skill == "" {
		req.Skill = skillFromModel
	}

	adapter := s.registry.Resolve(req.Model)
	if adapter == nil {
		http.Error(w, "unknown model: "+req.Model, http.StatusBadRequest)
		rec.Status = "error"
		rec.HTTPStatus = http.StatusBadRequest
		rec.Error = "unknown model"
		rec.DurationMS = time.Since(startTime).Milliseconds()
		s.audit.Request(rec)
		return
	}
	rec.Adapter = adapter.Name()
	if !adapter.Available() {
		http.Error(w, "adapter not implemented: "+adapter.Name(), http.StatusNotImplemented)
		rec.Status = "stub"
		rec.HTTPStatus = http.StatusNotImplemented
		rec.Error = "adapter not implemented"
		rec.DurationMS = time.Since(startTime).Milliseconds()
		s.metrics.RequestFinished(adapter.Name(), req.Model, "stub", rec.DurationMS, 0, 0)
		s.audit.Request(rec)
		return
	}

	// Apply skill prefix to the first user message if requested.
	if skill := strings.TrimSpace(req.Skill); skill != "" {
		req.Messages = applySkillPrefix(req.Messages, skill)
	}

	// Pick an account from the auth pool if configured.
	var pickedAccount *Account
	if s.auth != nil {
		if provider := providerForAdapter(adapter.Name()); provider != "" {
			if acc, err := s.auth.Pick(provider, req.AccountID); err == nil {
				pickedAccount = acc
			} else if req.AccountID != "" {
				http.Error(w, "auth pool: "+err.Error(), http.StatusBadRequest)
				return
			}
			// If no accounts configured for this provider, fall through with no key
			// (anthropic-sdk will then read its env var; claude-cli uses host login).
		}
	}

	// Concurrency cap
	select {
	case s.sem <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	defer func() { <-s.sem }()

	s.metrics.RequestStarted()

	// Resolve workspace: warm pool > existing workspace_id > inline create.
	var (
		ws       *Workspace
		wsID     string
		wsOwned  bool
	)
	if req.WorkspaceID != "" {
		stored, ok := s.wsStore.Get(req.WorkspaceID)
		if !ok {
			http.Error(w, "unknown workspace_id", http.StatusBadRequest)
			s.metrics.RequestFinished(adapter.Name(), req.Model, "error", time.Since(startTime).Milliseconds(), 0, 0)
			return
		}
		ws = stored
		wsID = req.WorkspaceID
		stored.Touch()
	} else {
		baseRepo := s.cfg.BaseRepo
		if req.BaseRepo != "" {
			baseRepo = req.BaseRepo
		}
		if baseRepo == s.cfg.BaseRepo {
			if pooled := s.pool.Get(); pooled != nil {
				ws = pooled
			}
		}
		if ws == nil {
			created, err := newWorkspace(s.cfg.WorkspaceRoot, baseRepo, requestID, s.cfg.RetainWorkspaces)
			if err != nil {
				http.Error(w, "workspace: "+err.Error(), http.StatusInternalServerError)
				rec.Status = "error"
				rec.HTTPStatus = http.StatusInternalServerError
				rec.Error = err.Error()
				rec.DurationMS = time.Since(startTime).Milliseconds()
				s.audit.Request(rec)
				s.metrics.RequestFinished(adapter.Name(), req.Model, "error", rec.DurationMS, 0, 0)
				return
			}
			if err := created.SeedAuth(); err != nil {
				s.logger.Printf("seed auth [%s]: %v (continuing)", requestID, err)
			}
			ws = created
		}
		wsOwned = true
	}
	rec.WorkspaceID = wsID
	defer func() {
		if wsOwned && req.WorkspaceID == "" {
			if cleanupErr := ws.Cleanup(); cleanupErr != nil {
				s.logger.Printf("workspace cleanup [%s]: %v", requestID, cleanupErr)
			}
		}
	}()

	agentReq := AgentRequest{
		Messages:           req.Messages,
		Model:              req.Model,
		WorkspacePath:      ws.Path,
		ClaudeConfig:       ws.ClaudeDir,
		RequestID:          requestID,
		TimeoutSec:         s.cfg.TimeoutSeconds,
		SystemPrompt:       req.SystemPrompt,
		AppendSystemPrompt: req.AppendSystemPrompt,
		Agent:              req.Agent,
		PermissionMode:     req.PermissionMode,
		ClaudeModel:        req.ClaudeModel,
		Effort:             req.Effort,
		AllowedTools:       req.AllowedTools,
		DisallowedTools:    req.DisallowedTools,
		MCPConfig:          req.MCPConfig,
		AddDirs:            req.AddDirs,
		Bare:               req.Bare,
		IncludeHookEvents:  req.IncludeHookEvents,
		BaseURL:            s.cfg.AnthropicBaseURL,
		MaxTokens:          0,
		Temperature:        0,
	}
	if req.Temperature != nil {
		agentReq.Temperature = *req.Temperature
	}
	if req.MaxTokens != nil {
		agentReq.MaxTokens = *req.MaxTokens
	}
	if pickedAccount != nil {
		agentReq.APIKey = pickedAccount.ResolveKey()
		agentReq.ClaudeCredentialsPath = pickedAccount.ClaudeCredentialsPath
		agentReq.ClaudeConfigSourceDir = pickedAccount.ClaudeConfigDir

		// For claude-cli adapter with an OAuth account, re-seed the workspace's
		// claude-config dir using THIS account's credentials (overrides the
		// host login that the warm pool may have seeded).
		if adapter.Name() == "claude-cli" && (pickedAccount.ClaudeCredentialsPath != "" || pickedAccount.ClaudeConfigDir != "") {
			if err := ws.SeedAuthFrom(pickedAccount.ClaudeConfigDir, pickedAccount.ClaudeCredentialsPath); err != nil {
				s.logger.Printf("seed account %s creds: %v", pickedAccount.ID, err)
			}
		}
	}
	rec.AccountID = ""
	if pickedAccount != nil {
		rec.AccountID = pickedAccount.ID
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if req.Stream {
		s.streamCompletion(ctx, w, adapter, req, agentReq, ws, wsID, requestID, startTime, &rec)
		return
	}
	s.bufferedCompletion(ctx, w, adapter, req, agentReq, ws, wsID, requestID, startTime, &rec)
}

func (s *Server) streamCompletion(
	ctx context.Context, w http.ResponseWriter, adapter AgentAdapter,
	req ChatCompletionRequest, agentReq AgentRequest, ws *Workspace, wsID, requestID string,
	startTime time.Time, rec *RequestRecord,
) {
	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stopKeepalive := sse.startKeepalive(ctx, time.Duration(s.cfg.KeepaliveSeconds)*time.Second)

	events := make(chan AgentEvent, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		defer close(events)
		runErr = adapter.Run(ctx, agentReq, events)
	}()

	created := time.Now().Unix()
	model := req.Model
	chunkID := "chatcmpl-" + requestID

	_ = sse.Send(ChatCompletionChunk{
		ID: chunkID, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []Choice{{Index: 0, Delta: &Delta{Role: "assistant"}}},
	})

	var stopReason string
	var numTurns int
	var costUSD float64
	var inputTokens, outputTokens int
	toolNames := map[int]string{}

streamLoop:
	for {
		select {
		case <-ctx.Done():
			break streamLoop
		case ev, ok := <-events:
			if !ok {
				break streamLoop
			}
			switch ev.Type {
			case EventTextDelta:
				_ = sse.Send(ChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []Choice{{Index: 0, Delta: &Delta{Content: ev.Content}}},
				})
			case EventToolCallStart:
				if ev.Tool == nil {
					continue
				}
				toolNames[ev.Tool.Index] = ev.Tool.Name
				s.metrics.ToolInvoked(ev.Tool.Name)
				_ = sse.Send(ChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []Choice{{Index: 0, Delta: &Delta{
						ToolCalls: []ToolCallDelta{{
							Index: ev.Tool.Index,
							ID:    ev.Tool.ID,
							Type:  "function",
							Function: &ToolFunctionDelta{
								Name:      ev.Tool.Name,
								Arguments: "",
							},
						}},
					}}},
				})
			case EventToolCallDelta:
				if ev.Tool == nil || ev.Tool.PartialJSON == "" {
					continue
				}
				_ = sse.Send(ChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []Choice{{Index: 0, Delta: &Delta{
						ToolCalls: []ToolCallDelta{{
							Index: ev.Tool.Index,
							Function: &ToolFunctionDelta{
								Arguments: ev.Tool.PartialJSON,
							},
						}},
					}}},
				})
			case EventToolCallEnd:
				// no-op on the wire; OpenAI clients accumulate by index until finish_reason
			case EventError:
				_ = sse.Send(ChatCompletionChunk{
					ID: chunkID, Object: "chat.completion.chunk", Created: created, Model: model,
					Choices: []Choice{{Index: 0, Delta: &Delta{Content: "\n[error: " + ev.Error + "]"}}},
				})
			case EventDone:
				stopReason = ev.StopReason
				numTurns = ev.NumTurns
				costUSD = ev.CostUSD
				inputTokens = ev.InputTokens
				outputTokens = ev.OutputTokens
			}
		}
	}
	wg.Wait()
	stopKeepalive()

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		s.logger.Printf("adapter run error [%s]: %v", requestID, runErr)
		rec.Error = runErr.Error()
	}
	picked := s.pickedAccountFor(rec)
	recordOutcome(picked, runErr)
	if picked != nil && runErr == nil {
		picked.RecordUsage(inputTokens, outputTokens, 0, 0)
		// USD-equivalent: claude CLI reports total_cost_usd directly.
		// Add it to the window's USD-consumed counter.
		if costUSD > 0 {
			picked.AddWindowUSD(costUSD)
		}
	}

	finish := mapStop(stopReason)
	diff, _ := ws.GitDiff()
	_ = sse.Send(ChatCompletionChunk{
		ID: chunkID, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []Choice{{Index: 0, Delta: &Delta{}, FinishReason: &finish}},
	})

	durationMS := time.Since(startTime).Milliseconds()
	extras := map[string]any{
		"kronaxis": KronaxisExtras{
			WorkspacePath: ws.Path,
			GitDiff:       diff,
			NumTurns:      numTurns,
			StopReason:    stopReason,
			DurationMS:    durationMS,
			Adapter:       adapter.Name(),
			WorkspaceID:   wsID,
		},
		"usage": Usage{CostUSD: costUSD},
	}
	_ = sse.Send(extras)
	sse.Done()

	status := "ok"
	if rec.Error != "" {
		status = "error"
	}
	rec.Status = status
	rec.HTTPStatus = http.StatusOK
	rec.NumTurns = numTurns
	rec.StopReason = stopReason
	rec.CostUSD = costUSD
	rec.InputTokens = inputTokens
	rec.OutputTokens = outputTokens
	rec.DurationMS = durationMS
	s.audit.Request(*rec)
	s.metrics.RequestFinished(adapter.Name(), req.Model, status, durationMS, costUSD, numTurns)
}

func (s *Server) bufferedCompletion(
	ctx context.Context, w http.ResponseWriter, adapter AgentAdapter,
	req ChatCompletionRequest, agentReq AgentRequest, ws *Workspace, wsID, requestID string,
	startTime time.Time, rec *RequestRecord,
) {
	events := make(chan AgentEvent, 32)
	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		defer close(events)
		runErr = adapter.Run(ctx, agentReq, events)
	}()

	var content strings.Builder
	type buildingTool struct {
		ID, Name, Args string
	}
	tools := map[int]*buildingTool{}
	var toolOrder []int

	var stopReason string
	var numTurns int
	var costUSD float64
	var inputTokens, outputTokens int

	for ev := range events {
		switch ev.Type {
		case EventTextDelta:
			content.WriteString(ev.Content)
		case EventToolCallStart:
			if ev.Tool == nil {
				continue
			}
			tools[ev.Tool.Index] = &buildingTool{ID: ev.Tool.ID, Name: ev.Tool.Name}
			toolOrder = append(toolOrder, ev.Tool.Index)
			s.metrics.ToolInvoked(ev.Tool.Name)
		case EventToolCallDelta:
			if ev.Tool == nil {
				continue
			}
			if t, ok := tools[ev.Tool.Index]; ok {
				t.Args += ev.Tool.PartialJSON
			}
		case EventError:
			content.WriteString("\n[error: " + ev.Error + "]\n")
		case EventDone:
			stopReason = ev.StopReason
			numTurns = ev.NumTurns
			costUSD = ev.CostUSD
			inputTokens = ev.InputTokens
			outputTokens = ev.OutputTokens
		}
	}
	wg.Wait()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		s.logger.Printf("adapter run error [%s]: %v", requestID, runErr)
		rec.Error = runErr.Error()
	}
	picked := s.pickedAccountFor(rec)
	recordOutcome(picked, runErr)
	if picked != nil && runErr == nil {
		picked.RecordUsage(inputTokens, outputTokens, 0, 0)
		if costUSD > 0 {
			picked.AddWindowUSD(costUSD)
		}
	}

	var openAITools []ToolCall
	for _, idx := range toolOrder {
		t := tools[idx]
		openAITools = append(openAITools, ToolCall{
			ID:       t.ID,
			Type:     "function",
			Function: ToolFunction{Name: t.Name, Arguments: t.Args},
		})
	}

	finish := mapStop(stopReason)
	diff, _ := ws.GitDiff()
	durationMS := time.Since(startTime).Milliseconds()
	resp := ChatCompletionResponse{
		ID:      "chatcmpl-" + requestID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{{
			Index: 0,
			Message: &AssistantMessage{
				Role:      "assistant",
				Content:   content.String(),
				ToolCalls: openAITools,
			},
			FinishReason: &finish,
		}},
		Usage: &Usage{CostUSD: costUSD},
		Kronaxis: &KronaxisExtras{
			WorkspacePath: ws.Path,
			GitDiff:       diff,
			NumTurns:      numTurns,
			StopReason:    stopReason,
			DurationMS:    durationMS,
			Adapter:       adapter.Name(),
			WorkspaceID:   wsID,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)

	status := "ok"
	if rec.Error != "" {
		status = "error"
	}
	rec.Status = status
	rec.HTTPStatus = http.StatusOK
	rec.NumTurns = numTurns
	rec.StopReason = stopReason
	rec.CostUSD = costUSD
	rec.InputTokens = inputTokens
	rec.OutputTokens = outputTokens
	rec.DurationMS = durationMS
	s.audit.Request(*rec)
	s.metrics.RequestFinished(adapter.Name(), req.Model, status, durationMS, costUSD, numTurns)
}

func applySkillPrefix(msgs []ChatMessage, skill string) []ChatMessage {
	if !strings.HasPrefix(skill, "/") {
		skill = "/" + skill
	}
	out := make([]ChatMessage, len(msgs))
	copy(out, msgs)
	// prefix the first user message
	for i, m := range out {
		if m.Role == "user" {
			out[i].Content = skill + " " + m.Content
			return out
		}
	}
	out = append(out, ChatMessage{Role: "user", Content: skill})
	return out
}

func mapStop(claudeStop string) string {
	switch claudeStop {
	case "end_turn", "":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "stream_closed":
		return "stop"
	default:
		return claudeStop
	}
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pickedAccountFor resolves the *Account that was used for a given request,
// from the audit record's account_id. Used after the run completes to record
// success/failure on the right account.
func (s *Server) pickedAccountFor(rec *RequestRecord) *Account {
	if rec == nil || rec.AccountID == "" || s.auth == nil {
		return nil
	}
	provider := providerForAdapter(rec.Adapter)
	if provider == "" {
		return nil
	}
	a, err := s.auth.Pick(provider, rec.AccountID)
	if err != nil {
		return nil
	}
	return a
}

// fmt.Errorf reference kept for the linter-tolerant build.
var _ = fmt.Sprintf
