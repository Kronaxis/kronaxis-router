# Changelog

All notable changes to kronaxis-router. Format loosely follows [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added: queue-aware load balancing (ROADMAP Phase 1)

`server.queue_aware_routing: true` starts a `QueueScraper` (`queueaware.go`) that polls each vLLM backend's `/metrics` on `queue_scrape_interval` (default 5s) and records `vllm:num_requests_waiting` → `Backend.QueueDepth` and `vllm:num_requests_running` → `Backend.ActiveInference`. Balancing then minimises `QueueLoad()` (queued + running) instead of the proxy's own active-request count, so traffic flows to the least-loaded node. Composes with KV pinning: candidates are ordered by warm-cache depth first, then least-loaded within the equal-cache group — "route to the warmest cache, unless it's overloaded". Best-effort: a scrape failure leaves last-known values and never affects request handling. Exposed in `/api/backends` and `/health` as `queue_depth` / `active_inference`. Off by default.

### Added: content-aware + learned prompt compression

A content router (`compress.go`) that detects each prompt segment's type and applies the right compressor instead of one lexical pass over everything:

- **JSON** (`compress_json.go`): whitespace compaction, optional null/empty pruning, and array-of-objects tabularisation (`{"__cols__":[...],"__rows__":[[...]]}`) that hoists repeated keys once. Lossless/reversible.
- **Code** (`compress_code.go`): a string-literal-aware comment + blank-line stripper. Restricted to languages where it is provably safe (Go, C-family, JS/TS, Rust, Java, Python, SQL); bash/yaml deliberately excluded (`${var#x}`, heredocs). Never corrupts string contents.
- **Prose**: lexical passes, plus an optional **learned** compressor (`prose_compressor.go`) that calls a self-hosted LLMLingua-2 endpoint (`services/prose-compressor/`). Lossy; falls back to lexical on any error.
- **Always-on lossless tier**: JSON compaction + whitespace over all traffic (keeps comments, never substitutes).
- **CCR** (`ccr_store.go`, `ccr_http.go`): reversible compress-cache-retrieve. Oversized segments are stashed and replaced with a stub the model expands via the new `compress_retrieve` MCP tool / `GET /v1/compress/retrieve`. Elision is gated on client capability, so content is never dropped from a client that cannot retrieve it.

Measured (tiktoken cl100k): ~36% lossless across a mixed corpus, up to ~65% on JSON/code-heavy bulk, prose ~30%→~50% with the learned compressor.

Clean-room Go reimplementation of ideas from [headroom](https://github.com/chopratejas/headroom) (Apache-2.0); see `NOTICE`.

## [v0.3.0] -- 2026-05-10

Phase 1 to 3 roadmap items shipped. The router moves from sovereign tier routing proxy to full LLM control plane.

### Added: KV cache aware routing (Phase 1)

Radix tree per backend keyed on chunked prefix hashes. Routes preferentially to the backend whose tree has the deepest matching prefix (vLLM KV cache is presumed warm there). Falls through to least connections plus round robin when no candidate has a matching prefix. Atomic `lastSeen` for race free concurrent walks; 5 minute sweeper evicts stale subtrees; per tree node cap (default 10,000).

Config (per backend, opt in):

```yaml
backends:
  - name: vllm-node-1
    kv_pinning:
      enabled: true
      max_prefix_age_seconds: 600
      hash_chunk_tokens: 128
      max_nodes: 10000
```

Endpoint: `GET /api/kv-trees` returns per backend node count and max age.

### Added: Stateful sessions (Phase 1)

Server stored conversation transcripts. Client uploads the full messages array once via `X-Kronaxis-Session-Create: true`, gets back `X-Kronaxis-Session-ID: sess_...`, then sends only the new turn on subsequent calls. Postgres `kr_sessions` table auto created in `runMigrations`. TTL sweeper evicts idle sessions every 5 minutes; in memory hot cache absorbs consecutive turn bursts. Endpoints: `GET /v1/sessions`, `GET /v1/sessions/<id>`, `DELETE /v1/sessions/<id>`. Override TTL via `X-Kronaxis-Session-TTL: <seconds>`.

### Added: Anthropic cache breakpoint injection

When `BackendConfig.CacheBreakpoints: true`, the proxy injects `cache_control: {"type": "ephemeral"}` markers on the stable prefix (last system message plus last assistant message) before forwarding. Compounds with sessions: sessions store transcript on the gateway, breakpoints make the provider's own cache hit on the same prefix every turn. Off by default; only enable for Anthropic native or OpenRouter style backends.

### Added: Schema validated quality gates (Phase 2)

JSON schema validation on the cheap tier's output via `santhosh-tekuri/jsonschema/v5`. FNV keyed compile cache so subsequent validations skip the parse cost. On violation, transparently retries the request on the next backend in the chain. Compiled schemas bounded at 1024 entries with random eviction.

### Added: DPO export to JSONL (Phase 2)

Every quality gate fallback (cheap output rejected, expensive output chosen) emits a Direct Preference Optimisation training pair to a JSONL file. Non blocking buffered writer with a single drainer goroutine; redaction of named keys (default `api_key`, `password`); milestone audit logs every N pairs. Enable via `DPO_EXPORT_PATH` env var. Endpoint: `GET /api/dpo`.

### Added: Cost forecasting and shadow routing (Phase 3)

Cost forecaster does linear extrapolation from spent so far plus hours elapsed today; surfaces budget exhaustion ETA per service. Shadow router mirrors a configurable percentage of primary requests to a shadow backend in a goroutine, computes Jaccard word similarity, persists comparisons as JSONL. Primary request path untouched and never affected by shadow latency or errors. Endpoints: `GET /api/costs/forecast`, `GET /api/shadow/stats`.

### Added: Cost Lab UI

Single page dashboard at `/cost-lab`. Vanilla JS, dark theme, four tabs (Today / Forecast / Shadow / DPO). Auto refreshes every 10 seconds, hits the existing `/api/costs`, `/api/backends`, `/api/costs/forecast`, `/api/shadow/stats`, `/api/dpo` endpoints. Linked from the main UI nav at `/`.

### Added: Release engineering

- Static distroless Docker image at `agent-gateway/Dockerfile` (~26 MB)
- Full image at `Dockerfile.full` (debian:12 plus git plus tini) for worktree profiles
- Goreleaser produces deb, rpm, tar.gz, zip across linux/darwin/windows times amd64/arm64
- Homebrew tap formula hand committed via `scripts/publish-homebrew-formula.sh` post tag
- Goreleaser `brews:` section removed (was requiring HOMEBREW_TAP_TOKEN secret)

### Fixed

- `TestMCPToolCallHealthNoRouter` now uses an ephemeral port via `net.Listen("tcp", "127.0.0.1:0")` instead of a hard coded 19999; no longer flakes on hosts where 19999 is in use.
- Two real concurrency bugs from earlier deep testing:
  - YAML write race in `agent-gateway` profile registration (concurrent POSTs to same name caused HTTP 500 in roughly 1 of 10 tries; fixed with per request `os.CreateTemp` tmp files)
  - Subprocess cancellation leaked grandchildren (`exec.CommandContext` killed direct child only; fixed with `Setpgid` plus `syscall.Kill(-pgid, SIGKILL)` per OS via build tags)

### Test coverage

55+ unit tests added covering KV pinning, sessions, schema gates, DPO export, shadow routing, Jaccard similarity, Anthropic cache breakpoint injection. All pass under `go test -race ./...`.

## [v0.2.0] -- 2026-05-09

### Added: CLI-Agent Gateway expansion (framework + tiered registry)

The agent-gateway sub-service has graduated from a Claude-Code-specific wrapper into a framework that exposes any genuine TUI agent CLI behind a single OpenAI-compatible endpoint.

- **Tiered registry** of profiles. Built-in defaults shipped: `claude-cli`, `codex-cli`, `aider` (first-class, deep stream parsers) plus `gemini-cli`, `grok-cli`, `llm` (supported, generic stdout streamer). User overrides drop into `agent-gateway/agents/<name>.yaml` and hot-reload via fsnotify.
- **Universal account pool** generalised from the previous Anthropic-only pool. Pool config in `accounts.yaml`; provider-aware cooldowns; round-robin checkout; concurrent-safe leases. Env interpolation via `${VAR}` resolves at checkout time.
- **Profile-declared workspace** lifecycle: `worktree-ephemeral` (default for file-editing agents), `dir-ephemeral` (chat-class CLIs), `stateless`. Per-request `X-Kronaxis-Workspace` override available.
- **Submodel surface**: `model: <agent>/<submodel>` is parsed, validated against the profile's `submodel.allowed` allowlist, and substituted into the CLI's `--model` flag (or env, per profile). Profiles with `supports: false` reject submodel-suffixed requests with a clear 400.
- **Profile-declared graphify default**: agentic file-editing CLIs default to `off` (they manage their own context); chat-class CLIs default to `compress`. Per-request `X-Kronaxis-Graphify` header overrides.
- **Rule synthesis**: `kronaxis-router agents register <name>` writes a backend stanza pointing at the gateway and appends the agent to the matching tier rule (creates `tier-<n>-auto` if absent). Idempotent re-registration. Capability tags stashed as backend metadata for future capability-based rules.
- **New API surface**: `GET/POST /v1/agents`, `GET/DELETE /v1/agents/<name>`, `POST /v1/accounts/test`, extended `GET /v1/accounts` enumerating all pools.
- **CLI subcommand**: `kronaxis-router agents register|list|remove|test`.

Code shape: 4 new packages in `agent-gateway/` (registry / accounts / workspace / runner) + 4 first-class output parsers + 6 builtin profile YAMLs embedded via `embed.FS` + the new dispatch path in `server.go`. Router-side: `synth.go` (idempotent YAML mutation that preserves comments and unrelated keys) + `agents_cmd.go`. Test coverage: 50+ unit tests across the new packages, all running under `go test -race`.

### Changed: BSL 1.1 licence

The repository was relicensed from Apache 2.0 to the Business Source License 1.1, with the Licensed Work converting back to Apache 2.0 on 9 May 2031 (the Change Date). Source-available with a non-commercial Additional Use Grant; commercial production use before the Change Date requires a separate licence. See `LICENSE`.

## [Previously] -- 2026-05-08

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
