# Changelog

All notable changes to kronaxis-router. Format loosely follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased] -- 2026-05-08

### Added: graphify pre-stage

Token-saving retrieval-augmented generation that runs before classifier and cost routing, so its savings stack with cache + cost routing + batching.

- **Two modes** plus auto + off:
  - `augment` -- prepend a system message with retrieved chunks (use for thin prompts that need project context)
  - `compress` -- replace the largest fat user message with retrieved chunks (use for prompts that dump a whole file or doc)
  - `auto` -- heuristic on largest message size
  - `off` -- skip; pass-through unchanged
- **Selection precedence**: `X-Kronaxis-Graphify` header > `X-Kronaxis-Service` override (animus -> augment, vanguard -> compress, atlas -> auto, ...) > `graphify.default` in config.
- **Substrate**: `kr_chunks` table (id, source_path, chunk_idx, content, embedding VECTOR(N), metadata JSONB, source_mtime, ingested_at) with HNSW + GIN(content) + path indexes. Auto-created on startup with the embedder's dim.
- **Hybrid retrieval**: pgvector cosine + BM25 reranking, with configurable weight, min cosine similarity, and token budget. Drops weak matches to avoid noise injection.
- **Pluggable embedder**:
  - `local-st` (default): Python Flask + sentence-transformers Docker sidecar at `embedding-service/`. Default model `BAAI/bge-small-en-v1.5` (384 dim). Free, ~20 ms / embed. Bakes model into image layers; entrypoint populates the volume on first start so swaps are persisted.
  - `gemini`: text-embedding-004 batch API (768 dim).
  - `openai`: text-embedding-3-small (1536 dim).
- **CLI**: `kronaxis-router ingest <paths...> [--reset] [--exclude] [-v]`, `kronaxis-router graphify {stats,reset}`.
- **HTTP**: `POST /v1/retrieve`, `GET /api/graphify`, plus Prometheus counters at `/metrics` (`kronaxis_router_graphify_*`).
- **Live re-ingest**: optional fsnotify watcher (`graphify.watch_enabled`) re-ingests changed files within ~2 s of write/create.
- **Concurrency**: parallel walker goroutines feed worker goroutines that batch + embed + upsert. Default 4 workers.

Disabled by default. Bring up the embedding sidecar, run an ingest, flip `graphify.enabled: true` to opt in.

### Added: agent-gateway sub-service

OpenAI-compatible HTTP service at `agent-gateway/` (port 8055) that wraps CLI agents.

- **Three real adapters**:
  - `claude-cli` -- full agentic loop with skills/MCP/hooks/file-edits via stream-json subprocess
  - `anthropic-sdk` -- direct API for cheap stateless inference
  - `gemini-cli` -- basic stdout streaming
- **OpenAI surface**: `POST /v1/chat/completions` (streaming + buffered, proper `tool_calls` array deltas), `POST /v1/workspaces` (multi-turn persistent), `GET /v1/models`, `GET /v1/accounts`, `GET /healthz`, `GET /metrics`, `GET /api/live`, `GET /` (web UI).
- **Per-request features**: SSE keepalive heartbeats, full pass-through of Claude CLI flags (system_prompt, agent, model, effort, allowed_tools, disallowed_tools, mcp_config, add_dirs, bare, include_hook_events), skill routing via `model+skillname` suffix, ephemeral or persistent workspaces with isolated `CLAUDE_CONFIG_DIR`.
- **Multi-account auth pool**: round-robin across configured accounts, pin via `account_id`, auto-disable on rate-limit / auth / credit / transient errors with provider-aware cooldowns.
- **Operational**: warm pool (cold-start ~3 s -> ~0.3 s), TTL sweeper, JSON audit log per request, Prometheus metrics, live web UI, systemd unit.

22 Go files, ~3,063 LOC. Tests cover stream-json parser, model+skill split, skill prefix application, auth-pool classification + round-robin.

Lives in `agent-gateway/` as a separate Go module so the router stays focused on cost-routing.

### Changed

- `go.mod`: added `github.com/fsnotify/fsnotify v1.7.0` for the graphify file-system watcher.
- `proxy.go`: graphify pre-stage hook between cache miss and classifier.
- `metrics.go`: graphify Prometheus counters merged into `/metrics` output.
- `main.go`: graphify subcommands (`ingest`, `graphify {stats,reset}`); embedder probe + schema bootstrap on startup; optional in-process watcher.
- `config.yaml`: new `graphify:` section; commented sample for the `claude-code-agent` backend.

### Notes

- Both additions are disabled by default. The router behaves identically to v1.x when `graphify.enabled: false` and the agent-gateway service isn't running.
- No changes to existing routing rules, classifier behaviour, cost tracking, batch API, or web UI.

### Added: Claude Code OAuth subscription pooling (agent-gateway)

The agent-gateway's `claude-cli` adapter now pools multiple Claude Code (Pro/Max) OAuth subscriptions in addition to API keys. Each subscription is registered through a gated `agent-gateway claude-login --name <id>` flow that requires explicit `PERSONAL-USE-ONLY` confirmation and writes a `.personal-use-acknowledged` marker the auth-pool loader checks at startup. Provider-aware cooldowns: rate-limit on a `claude-cli` account triggers a 5-hour cooldown (matching the subscription window); API-key accounts stay at 5 minutes.

Per-account window stats are tracked: `window_requests`, `window_tokens_total`, `window_usd_equiv` (the API-equivalent dollar figure from claude CLI's `total_cost_usd`), with optional `window_max_usd` to route around accounts that have consumed a configured $-equivalent of subscription value. Surfaced at `GET /v1/accounts`.

ToS posture: pooling consumer subscriptions for personal individual use is permitted; multi-tenant or commercial use requires the `anthropic-sdk` adapter with paid API keys. The gateway enforces this with the setup gate.

---

## Earlier history

For commits before this changelog was added, see `git log` -- highlights:

- Backend failover with retry on transient errors
- Async batch API integration (50% off on 7 providers)
- LoRA adapter routing for vLLM multi-LoRA instances
- Quality gate with sample-rate validation
- Per-service budgets and rate limits
- A/B testing framework
- Embedded web UI
- MCP server integration for editor IDEs
