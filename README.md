<p align="center">
  <img src="assets/kronaxis-icon.svg" width="64" height="64" alt="Kronaxis">
</p>

<h1 align="center">Kronaxis Router</h1>

<p align="center">
  <strong>Tier-routing LLM proxy for sovereign + custom + frontier models. Sub-5ms decisions, 50&times; cheaper for 80% of requests, with the agentic CLIs you actually use bolted on as OpenAI endpoints.</strong>
</p>

<p align="center">
  <a href="LICENSE">BSL 1.1</a> &middot;
  <a href="https://kronaxis.co.uk/kronaxis-router">Project page</a> &middot;
  <a href="https://kronaxis.co.uk/research">Research microsite</a> &middot;
  <a href="https://kronaxis.co.uk/blog/llm-routing-cost-savings">Blog post</a> &middot;
  <a href="examples/">Examples</a> &middot;
  <a href="CHANGELOG.md">Changelog</a>
</p>

<p align="center">
  <a href="https://github.com/kronaxis/kronaxis-router/actions/workflows/build.yml"><img src="https://github.com/kronaxis/kronaxis-router/actions/workflows/build.yml/badge.svg" alt="Build"></a>
  <a href="https://goreportcard.com/report/github.com/kronaxis/kronaxis-router"><img src="https://goreportcard.com/badge/github.com/kronaxis/kronaxis-router" alt="Go Report Card"></a>
</p>

---

Routes every LLM request to the **cheapest backend that can do the job**: sovereign / custom / open-weight 7-9B for the 80% of requests that don't need a frontier model, frontier APIs only for the genuinely hard 20%. Picks in **under 5 ms**, compresses prompts via pgvector RAG before they leave the box, and wraps real terminal agent CLIs (Claude Code, Codex, Aider, Gemini, Grok, llm) so each one is a regular OpenAI endpoint.

```
your service ─POST /v1/chat/completions─▶ Kronaxis Router ─▶ sovereign vLLM (7-9B, 50× cheaper)
                                              │            ─▶ frontier API (Anthropic / OpenAI / Gemini)
                                              │            ─▶ agent-gateway → claude / codex / aider CLI
                                              ├─ tier-routes by rule (5 ms p50)
                                              ├─ caches deterministic responses
                                              ├─ batches bulk to async APIs (50% off)
                                              ├─ compresses fat context via pgvector RAG
                                              └─ rotates pooled accounts past per-account limits
```

## Why use it

- **Cost routing first.** YAML rules match on task type, service, tier, priority, content type. Send JSON extraction to a local 7B at 0.005 USD / 1M tokens; send hard reasoning to Gemini 2.5 Pro at 1.25; let the router decide. Hot-reloadable, no restart. **22,770 req/s, 5 ms p50, 9.9 MB binary** — the router itself adds 2-5 ms over the backend's actual latency.
- **Sovereign-first.** Local vLLM + Ollama backends are first-class, not afterthoughts. Run on your own GPUs (or a single Pi cluster) and only spend dollars when frontier reasoning is actually required. The pitch is *not* "cloud routing with discounts" — it's *use a sovereign 9B for 80% of work, frontier APIs for the rest*.
- **Token-saving RAG pre-stage.** A pgvector + sentence-transformers retrieval step runs *before* the classifier. Augment mode prepends top-k chunks to thin prompts; compress mode replaces fat context with retrieved excerpts (verified saving 3,945 tokens on a single 10 KB request in production smoke). Stacks multiplicatively with cost routing.
- **Real CLI agents as OpenAI endpoints.** A separate `agent-gateway` sub-service spawns the actual binaries: `claude` (stream-json, isolated worktree, full skills + MCP + hooks + file editing), `codex`, `aider`, plus `gemini-cli`, `grok-cli`, `llm` as supported tier. Six profiles ship built-in; drop a YAML to add your own. Each one is `model: <agent>` or `model: <agent>/<submodel>` on `/v1/chat/completions`.
- **Multi-account auth pool.** API-key pools (Anthropic / OpenAI / Gemini / xAI / Aider-multi / llm-multi) and Claude Code OAuth subscription pools (personal use only, ToS-gated at setup). Round-robin, pin by `account_id`, auto-disable on 429 with provider-aware cooldowns (5 min for API keys, 5 hours for OAuth subs to match the window).

## How it compares

| | Kronaxis Router | LiteLLM | OpenRouter | Helicone |
|---|---|---|---|---|
| OpenAI-compatible proxy | ✓ | ✓ | ✓ | ✓ (logs only) |
| Self-hosted single binary | ✓ (Go, 9.9 MB) | ✓ (Python) | hosted only | hosted only |
| Cost routing by rule | ✓ | ✓ | ✓ | ✗ |
| Multi-backend failover | ✓ | ✓ | ✓ | ✗ |
| **CLI agent wrapping (Claude Code, Gemini CLI)** | ✓ | ✗ | ✗ | ✗ |
| **Built-in RAG pre-stage (pgvector + ST)** | ✓ | ✗ | ✗ | ✗ |
| **Multi-account OAuth subscription pool** | ✓ (with ToS gate) | ✗ | ✗ | ✗ |
| Async batch API (50% off) | ✓ (7 providers) | ✓ | ✗ | ✗ |
| Response caching | ✓ | ✓ | ✗ | ✓ |
| Hot-reloadable config | ✓ | ✗ | n/a | n/a |
| Prometheus metrics | ✓ | ✓ | ✗ | ✓ |
| Embedded web UI | ✓ | ✓ | ✓ | ✓ (best UI in class) |
| Hosted SaaS option | ✗ | ✗ (cloud add-on) | ✓ (their model) | ✓ |
| LangChain integrations adaptor | via OpenAI base URL | native | native | native |

If you want a hosted "credits + many providers" experience, OpenRouter wins. If you want the best logging dashboard, Helicone. If you want a Python-native multi-provider client, LiteLLM. If you want to **run the proxy yourself, route by cost, and treat Claude Code as an API**, this is the only option that ships those things in one binary.

## Part of the Kronaxis stack

Kronaxis Router is the infrastructure layer of a source-available stack covering psychographics, synthetic-panel simulation, public proof, and LLM ops:

1. [**DYNAMICS-8**](https://github.com/Kronaxis/dynamics-8) — eight-dimension psychographic framework with two new digital-age dimensions (CC BY 4.0 spec)
2. [**Panel Studio**](https://github.com/Kronaxis/kronaxis-panel-studio) — synthetic consumer panels engine; 1,000 personas in 30 seconds
3. [**KPM-1**](https://github.com/Kronaxis/kpm1-election-projections) — pre-registered, hash-verified election predictions (the public proof the stack works)
4. **Kronaxis Router** (this repo) — the tier-routing LLM proxy + agent gateway
5. [**Kronaxis Fabric**](https://github.com/Kronaxis/kronaxis-fabric) — the memory + coord + orchestrator companion. One Go binary on Postgres. Pairs directly with Router: **Router decides WHICH model the request goes to; Fabric decides WHAT context goes into it.** Together you stop paying frontier prices for context you didn't need to send to a model that didn't need to be that big. The typical "how does X work in this codebase" turn drops from $0.60 (frontier + 120K-token preload) to $0.001 (sovereign 7B + 2KB of relevant memos) when both run together. See the [pairing blog post](https://kronaxis.co.uk/blog/agent-context-memory-fabric).

**Each piece is independently usable.** Kronaxis Router runs fine without any of the others — it's a general-purpose tier-routing proxy that any team running mixed sovereign + frontier LLM workloads can drop in. Add Fabric when your agent context preloads get too large to stomach.

The router exists because, in our own work, our personas run on custom and sovereign LLMs but the management layer needs more — observability, cost ceilings, fallback chains, agent invocation, account pooling, RAG compression. Specifically: running 65,000-persona panels through frontier-API pricing is uneconomical — small open-weight models handle 80% of those requests identically and 50× cheaper, **but only if something can route the request to the right tier in under 5 ms**. That's this. Same shape applies to any large-scale agent / synthetic-data / batch-extraction workload.

## 60-second quickstart

```bash
# Just the agent-gateway (Claude Code as OpenAI). Nothing else needed.
go install github.com/kronaxis/agent-gateway@latest
claude auth login                 # if you haven't already
agent-gateway -config /dev/stdin <<'EOF'
port: 8055
claude_binary: "claude"
EOF

# Send a request
curl -N http://localhost:8055/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-code-agent","stream":true,
       "messages":[{"role":"user","content":"write hello.go that prints kronaxis"}]}'
```

The final SSE chunk carries a `kronaxis` extras object with the file diff the agent produced. See [`agent-gateway/`](agent-gateway/) for full docs.

For the cost-routing proxy with cloud + local backends, see the [examples/](examples/) directory.

## Features (full list)

- **Cost-optimised routing** -- YAML rules match on task type, service, tier, priority, and content type. Route to the cheapest capable backend.
- **Multi-backend support** -- Local vLLM, Gemini, OpenAI, Ollama. Mix local GPUs with cloud APIs. Automatic format adaptation.
- **LoRA adapter routing** -- Knows which vLLM instances have which adapters loaded. Routes role-specific requests to the right instance.
- **Backend failover** -- If the first backend returns 5xx or times out, automatically tries the next in the chain. Retry with backoff on transient errors.
- **Throughput batching** -- Background/bulk requests collected over a 50ms window and dispatched as a single multi-prompt `/v1/completions` call to vLLM. Improves GPU utilisation on self-hosted models.
- **Cost-saving batch API** -- Submit bulk work to provider batch APIs (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks) for **50% off** standard pricing. Async processing, typically completes in minutes. Auto-routes `bulk` priority requests.
- **Response caching** -- SHA-256 keyed cache for deterministic requests (temperature=0). Identical prompts served from cache without calling the backend.
- **Per-service budgets** -- Daily cost limits per calling service. Exceeding a budget triggers downgrade (cheaper model) or rejection.
- **Per-service rate limiting** -- Token bucket rate limiter per caller. Configurable requests/second and burst size.
- **Prometheus metrics** -- `/metrics` endpoint with request counts, latency histograms, error rates, backend health, cache stats.
- **Health checks & failover** -- 30-second health probes. Error tracking from actual requests (including cloud APIs).
- **Queue-aware load balancing** -- Optional (`server.queue_aware_routing: true`). Scrapes each vLLM backend's `/metrics` (`num_requests_waiting` + `num_requests_running`) and routes to the least-loaded node. Composes with KV pinning: warmest cache first, then least-loaded within equal-cache — route to the warmest cache unless it's overloaded.
- **Streaming pass-through** -- SSE forwarding for real-time use cases (voice, chat).
- **Qwen3 thinking mode** -- Auto-disables thinking mode and strips `<think>` tags for Qwen3/3.5 models.
- **Hot-reloadable config** -- Edit `config.yaml` and rules update within 5 seconds. No restart needed.
- **Embedded web UI** -- Dashboard, visual flow builder, backend manager, cost analysis, config editor.
- **API authentication** -- Bearer token auth on `/api/*` endpoints via `ROUTER_API_TOKEN` env var.
- **OpenAI API compatible** -- Drop-in replacement. Services change one URL.
- **Graphify pre-stage (RAG)** -- Optional middleware that runs *before* every backend. Replaces fat context with retrieved chunks (compress mode) or augments thin prompts with project context (augment mode). Backed by pgvector + a swappable embedder (default: local sentence-transformers in a Docker sidecar; alternatives: Gemini, OpenAI). Stacks with cost routing and caching for compounding token savings. See `embedding-service/` and `kronaxis-router ingest`.
- **Content-aware compression** -- Detects each prompt segment's type and applies the right compressor instead of one lexical pass: JSON compaction + array-of-objects tabularisation, string-literal-aware code comment stripping (safe languages only), and prose passes. An always-on **lossless** tier (JSON + whitespace, keeps comments, never substitutes) runs on all traffic; an aggressive opt-in tier adds null-pruning, tabularisation, comment stripping, a learned **LLMLingua-2 prose compressor** (self-hosted GPU sidecar, see `services/prose-compressor/`), and reversible **CCR** elision (oversized blocks stubbed + expandable via the `compress_retrieve` tool / `GET /v1/compress/retrieve`, gated on client capability so nothing is dropped from a client that can't fetch it back). Measured (tiktoken cl100k): ~36% lossless, up to ~65% on JSON/code-heavy bulk, prose ~30%→~50% learned. Clean-room reimplementation of [headroom](https://github.com/chopratejas/headroom) ideas (Apache-2.0); see `NOTICE`. Config under `graphify` (`structural_compress`, `json_tabularize`, `ccr_enabled`, `prose_compressor`).
- **Agent Gateway** -- Optional sub-service at `agent-gateway/` (port 8055). Wraps CLI agents (Claude Code, Anthropic SDK, Gemini CLI) as OpenAI-compatible endpoints. Persistent named workspaces for multi-turn, warm pool for ~0.3s cold-start, JSON audit log, Prometheus metrics, live UI, multi-account auth pool with auto-disable on rate limits. Each request can run a real agentic loop in an isolated git worktree and return the diff alongside the assistant text.

## Install

```bash
# One-line install (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | sh

# Homebrew
brew install kronaxis/tap/kronaxis-router

# Go
go install github.com/kronaxis/kronaxis-router@latest

# Docker
docker run -p 8050:8050 ghcr.io/kronaxis/kronaxis-router:latest
```

## Quick Start

```bash
# Auto-detect local models and API keys, generate config
kronaxis-router init

# Start the router
kronaxis-router

# Dashboard at http://localhost:8050
```

The `init` command probes for Ollama (localhost:11434), vLLM (localhost:8000), and cloud API keys in your environment (`GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GROQ_API_KEY`, `TOGETHER_API_KEY`, `FIREWORKS_API_KEY`). It generates a `config.yaml` with backends, routing rules, budgets, and rate limits.

Point your services at `http://localhost:8050/v1/chat/completions` instead of calling LLM backends directly.

## Tool Integration

```bash
kronaxis-router init --aider      # Aider: sets OPENAI_API_BASE
kronaxis-router init --continue    # Continue.dev: generates config.json snippet
kronaxis-router init --cursor      # Cursor: generates MCP config
kronaxis-router init --claude       # Claude Code: configures MCP server in ~/.claude/settings.json
kronaxis-router init --openwebui   # Open WebUI: prints connection settings
```

## MCP Server (Claude Code, Cursor, Claude Desktop)

The router includes a built-in [MCP](https://modelcontextprotocol.io) server that gives AI assistants tools to manage routing, costs, and backends conversationally.

```bash
# One-time setup for Claude Code
kronaxis-router init --claude

# Or manually add to ~/.claude/settings.json:
{
  "mcpServers": {
    "kronaxis-router": {
      "command": "kronaxis-router",
      "args": ["mcp"],
      "env": {
        "ROUTER_URL": "http://localhost:8050"
      }
    }
  }
}
```

Available MCP tools:

| Tool | Purpose |
|------|---------|
| `router_health` | Backend statuses, uptime, cache stats |
| `router_backends` | List all backends with health and costs |
| `router_costs` | Daily spending by service/model |
| `router_stats` | Live request metrics |
| `router_rules` | List routing rules |
| `router_add_backend` | Register a new LLM endpoint |
| `router_remove_backend` | Remove a backend |
| `router_add_rule` | Create a routing rule |
| `router_remove_rule` | Remove a rule |
| `router_update_budget` | Set daily spending limits |
| `router_config` | View full YAML config |
| `router_reload` | Force config reload |

### Build from source

```bash
git clone https://github.com/kronaxis/kronaxis-router.git
cd kronaxis-router
go build -o kronaxis-router .
./kronaxis-router
```

## Usage Examples

### Send a request (routes to cheapest capable backend)

```bash
curl http://localhost:8050/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Kronaxis-Service: my-api" \
  -H "X-Kronaxis-CallType: summarise" \
  -H "X-Kronaxis-Tier: 2" \
  -d '{
    "model": "default",
    "messages": [{"role": "user", "content": "Summarise this in one sentence: ..."}],
    "max_tokens": 100
  }'
```

### Route heavy reasoning to the large model

```bash
curl http://localhost:8050/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Kronaxis-Service: my-api" \
  -H "X-Kronaxis-Tier: 1" \
  -d '{
    "model": "default",
    "messages": [{"role": "user", "content": "Plan a 3-phase migration strategy for..."}],
    "max_tokens": 2000
  }'
```

### Submit bulk work for 50% off (async batch API)

```bash
curl -X POST http://localhost:8050/api/batch/submit \
  -H "Content-Type: application/json" \
  -d '{
    "backend": "cloud-fast",
    "callback_url": "https://my-app.com/webhook",
    "requests": [
      {"custom_id": "req-1", "body": {"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "..."}], "max_tokens": 100}},
      {"custom_id": "req-2", "body": {"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "..."}], "max_tokens": 100}}
    ]
  }'
```

### Check cost dashboard

```bash
curl http://localhost:8050/api/costs?period=today
```

### Check Prometheus metrics

```bash
curl http://localhost:8050/metrics
```

### Check backend health

```bash
curl http://localhost:8050/health
```

## How Routing Works

1. Request arrives at `/v1/chat/completions` (OpenAI-compatible)
2. Router extracts metadata from `X-Kronaxis-*` headers and request body
3. Rules are evaluated in priority order (highest first)
4. Each rule's backend list is filtered by health, capabilities, LoRA adapters, and cost ceiling
5. First healthy, capable backend wins
6. If no rule matches, the default fallback chain is used

### Routing Metadata (Headers)

| Header | Purpose | Example |
|--------|---------|---------|
| `X-Kronaxis-Service` | Calling service name | `my-api` |
| `X-Kronaxis-CallType` | Task type for rule matching | `summarise`, `classify` |
| `X-Kronaxis-Priority` | `interactive` / `normal` / `background` / `bulk` | `background` |
| `X-Kronaxis-Tier` | Capability tier (1=heavy, 2=light) | `2` |
| `X-Kronaxis-PersonaID` | Cost attribution | `user-123` |

Headers are optional. Without them, the router uses default rules and the fallback chain.

When `X-Kronaxis-Tier` is unset, the router auto-classifies request complexity (0–100) and picks the tier itself. The score is surfaced as the `X-Kronaxis-Complexity` response header.

## Cluster Intelligence (multi-vLLM)

Two routing optimisations for anyone running more than one vLLM node. They compose: **route to the warmest cache, unless it's overloaded.**

### KV cache-aware routing (radix-tree pinning)

Round-robin across a vLLM cluster forces each node to recompute the KV cache for multi-turn conversations — a 100k-token system prompt gets reprocessed on every turn that lands on a different node. The router keeps a per-backend radix tree of recently-seen prompt-prefix hashes and biases routing toward the node whose cache is already warm for that prefix. TTFT drops from seconds to milliseconds on a cache hit.

Enable per backend:

```yaml
backends:
  - name: vllm-node-1
    url: "http://gpu-1:8000"
    type: vllm
    kv_pinning:
      enabled: true
      max_prefix_age_seconds: 600   # forget prefixes older than 10 min
      hash_chunk_tokens: 128        # prefix-hash granularity
      max_nodes: 10000              # per-backend tree cap
```

Inspect the live trees at `GET /api/kv-trees`.

### Queue-aware load balancing

Health checks tell you a node is alive, not whether it's busy. With queue-aware routing on, the router scrapes each vLLM backend's `/metrics` (`vllm:num_requests_waiting` + `vllm:num_requests_running`) and prefers the least-loaded node. Stacked with KV pinning, candidates are ordered by warm-cache depth first, then least-loaded within the equal-cache group. A backend that is never successfully scraped (non-vLLM, or unreachable `/metrics`) falls back to its proxy active-request count, so it can never masquerade as idle.

```yaml
server:
  queue_aware_routing: true     # default: false
  queue_scrape_interval: 5s     # how often to poll /metrics
```

Per-backend `queue_depth` and `active_inference` are exposed in `GET /api/backends` and `GET /health`. Best-effort: a scrape failure never affects request handling.

## Stateful Sessions

Agentic clients (Claude Code, Cursor, custom agents) re-upload the whole conversation on every turn. Sessions let the client upload the full context **once**; the router stores it and hydrates the full prompt server-side on subsequent turns from just a session ID. This also lets the router inject provider cache breakpoints at the optimal static/dynamic boundary.

```bash
# First turn: upload full context, get a session id back
curl http://localhost:8050/v1/chat/completions \
  -H "X-Kronaxis-Session-Create: true" \
  -d '{"messages":[{"role":"system","content":"...100k tokens..."}]}'
# Response header: X-Kronaxis-Session-ID: sess_abc123

# Later turns: send only the id + the new message
curl http://localhost:8050/v1/chat/completions \
  -H "X-Kronaxis-Session-ID: sess_abc123" \
  -d '{"messages":[{"role":"user","content":"just the new question"}]}'
```

Headers: `X-Kronaxis-Session-Create`, `X-Kronaxis-Session-ID`, `X-Kronaxis-Session-TTL` (request); `X-Kronaxis-Session-Created` (response). Manage sessions at `GET/DELETE /v1/sessions[/<id>]`. Requires `DATABASE_URL` (sessions live in the `kr_sessions` Postgres table with a TTL sweeper + hot cache).

## Cost-Saving Principles

The default `config.yaml` demonstrates six principles:

1. **Structured extraction -> small model.** JSON parsing, classification, scoring. A 7-9B model handles these as well as a 70B.
2. **Heavy reasoning -> large model.** Planning, multi-step logic, creative writing. Only these justify the cost.
3. **Bulk work -> cheapest available.** Latency doesn't matter; cost does.
4. **Interactive work -> fastest available.** Skip batching, accept higher cost for responsiveness.
5. **Vision tasks -> vision-capable backends only.** Don't waste attempts on blind backends.
6. **Budget overflow -> downgrade, don't fail.** When the budget is hit, route to a cheaper model instead of returning errors.

## Configuration

See `config.yaml` for the full reference. Key sections:

### Backends

```yaml
backends:
  - name: my-local-gpu
    url: "http://localhost:8000"
    type: vllm                     # vllm, gemini, ollama, openai
    model_name: "my-model"
    cost_input_1m: 0.01            # USD per 1M input tokens
    cost_output_1m: 0.01           # USD per 1M output tokens
    capabilities: [json_output]    # json_output, long_context, vision, lora_adapter
    max_concurrent: 4
    lora_adapters: [adapter-a, adapter-b]
```

### Routing Rules

```yaml
rules:
  - name: cheap-extraction
    priority: 120                  # Higher = evaluated first
    match:
      tier: 2                      # Match tier 2 requests
    backends: [small-model, large-model, cloud-fallback]
    max_cost_1m: 0.50              # Only use backends cheaper than $0.50/1M
```

### Budgets

```yaml
budgets:
  my-api:
    daily_limit_usd: 50.00
    action: downgrade              # "downgrade" or "reject"
    downgrade_target: small-model
```

## API Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/chat/completions` | POST | OpenAI-compatible proxy (main endpoint) |
| `/v1/compress/retrieve?id=X` | GET | Fetch the original of a CCR-elided block (`?format=json` for metadata) |
| `/health` | GET | Health check with backend statuses |
| `/api/costs` | GET | Cost dashboard (daily/weekly/monthly breakdown) |
| `/api/backends` | GET | List all backends and their status |
| `/api/backends` | POST | Register a dynamic backend |
| `/api/backends?name=X` | DELETE | Remove a dynamic backend |
| `/api/config` | GET | View current routing config summary |
| `/api/batch/submit` | POST | Submit async batch job (50% off) |
| `/api/batch` | GET | List all batch jobs or get status by `?id=` |
| `/api/batch/results` | GET | Retrieve results of a completed batch |
| `/api/batch/stream` | GET | SSE stream for batch job updates |
| `/api/rules` | GET/POST/PUT/DELETE | CRUD for routing rules |
| `/api/budgets` | GET/PUT | View/update per-service budgets |
| `/api/config/yaml` | GET/PUT | View/update raw YAML config |
| `/api/config/reload` | POST | Force config reload from disk |
| `/api/stats` | GET | Live request statistics |
| `/v1/sessions` / `/v1/sessions/<id>` | GET/DELETE | List/inspect/delete stateful sessions |
| `/api/kv-trees` | GET | Inspect the per-backend KV-cache prefix trees |
| `/api/costs/forecast` | GET | Per-service budget burn-rate forecast |
| `/api/shadow/stats` | GET | Shadow-routing comparison stats |
| `/api/dpo` | GET | DPO preference-pair export status |
| `/metrics` | GET | Prometheus metrics |
| `/` | GET | Embedded web UI |

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `CONFIG_PATH` | `config.yaml` | Path to configuration file |
| `ROUTER_PORT` | `8050` | HTTP listen port |
| `DATABASE_URL` | (empty) | PostgreSQL connection string for cost logging |
| `ROUTER_API_TOKEN` | (empty) | Bearer token for `/api/*` auth. Unset = open access. |
| `CACHE_MAX_SIZE` | `1000` | Max cached responses (0 = disabled) |
| `CACHE_TTL_SECONDS` | `3600` | Cache entry TTL in seconds |
| `BATCH_DATA_DIR` | `/tmp/kronaxis-router-batches` | Directory for batch job data |
| `GEMINI_API_KEY` | (empty) | Referenced via `env:GEMINI_API_KEY` in config |

## Rate Limiting

Per-service request rate limits, configured in `config.yaml`:

```yaml
rate_limits:
  default:
    requests_per_second: 100
    burst_size: 200
  batch-worker:
    requests_per_second: 10
    burst_size: 20
```

Only the `/v1/chat/completions` endpoint is rate limited. API and UI endpoints are not.

## Response Headers

Every response includes (when branding is enabled):

```
X-Powered-By: Kronaxis Router
X-Kronaxis-Router-Version: 1.0.0
X-Kronaxis-Backend: local-large
X-Kronaxis-Rule: heavy-reasoning
X-Kronaxis-Cache: HIT          # only on cache hits
X-Kronaxis-Graphify: lossless  # compression/RAG mode applied: lossless|compress|augment|off
X-Kronaxis-Graphify-Tokens-Saved: 6   # approx tokens saved by compression
```

To opt a request into the aggressive (lossy) compression tier, send
`X-Kronaxis-Graphify: compress`. CCR elision additionally requires
`X-Kronaxis-Compress-CCR: 1` or an allowlisted `X-Kronaxis-Service`.

## Database (Optional)

If `DATABASE_URL` is set, the router logs all requests to the `llm_call_log` table for cost analysis. The router auto-creates the required `service` column on startup.

Without a database, the router works fully -- cost tracking happens in memory only and resets on restart.

## Graphify Pre-Stage (RAG)

Token-saving retrieval-augmented generation that runs **before** classifier + cost routing, so its savings compound across every backend.

Two modes, plus auto:

- **augment** -- prepend a system message with top-K retrieved chunks. Use for thin prompts (LLM gets relevant project context). Default budget: ~800 tokens of context.
- **compress** -- replace the largest non-system message with retrieved chunks. Use for fat prompts (replaces a 30 kB file dump with 1 kB of relevant excerpts). Default budget: ~1200 tokens.
- **auto** -- pick based on the largest message size: large → compress, small → augment, medium → off.
- **off** -- skip; pass through unchanged.

Selected via `X-Kronaxis-Graphify: compress|augment|auto|off` per request, or globally via `graphify.default` in config.

### Architecture

```
ingest                                                   request
  ↓                                                         ↓
files → chunker → embedder (sidecar) → pgvector kr_chunks ← retrieve (cosine + BM25)
                                                            ↓
                                                       compress / augment messages
                                                            ↓
                                                       classifier → backend
```

### Embedder backends

| Type | Default model | Dim | Notes |
|---|---|---|---|
| `local-st` (default) | `BAAI/bge-small-en-v1.5` | 384 | Docker sidecar, free, ~20ms/embed |
| `gemini` | `text-embedding-004` | 768 | Cloud, ~$0.00001 / 1k tokens, 5ms |
| `openai` | `text-embedding-3-small` | 1536 | Cloud |

Switch via `graphify.embedder.type` in config. Changing dim requires `kronaxis-router graphify reset` then re-ingest.

### Bring it up

```bash
# 1. Start the embedding sidecar (Docker; or `python embedding-service/server.py` for local)
docker compose up -d embedding-service

# 2. Ingest a project (chunks → embed → upsert to pgvector)
DATABASE_URL=postgres://... kronaxis-router ingest /path/to/repo

# 3. Enable in config.yaml: graphify.enabled: true, graphify.default: "auto"

# 4. Per-request override
curl http://localhost:8050/v1/chat/completions \
  -H 'X-Kronaxis-Graphify: augment' \
  -H 'Content-Type: application/json' \
  -d '{"model":"...", "messages":[{"role":"user","content":"how does the auth handler work?"}]}'

# Response includes:
#   X-Kronaxis-Graphify: augment
#   X-Kronaxis-Graphify-Chunks: 5
#   X-Kronaxis-Graphify-Tokens-Saved: 1840   (compress mode only)
```

### Endpoints

- `POST /v1/retrieve` -- raw retrieval, returns top-K scored chunks. Useful for debugging or external RAG.
- `GET /api/graphify` -- counters: requests, augments, compresses, chunks retrieved, tokens saved, errors.
- `/metrics` -- Prometheus counters: `kronaxis_router_graphify_*`.

### CLI

- `kronaxis-router ingest <paths...> [--reset] [--exclude name1,name2] [-v]` -- ingest into pgvector.
- `kronaxis-router graphify stats` -- row count + token totals.
- `kronaxis-router graphify reset` -- drop `kr_chunks` (use when changing embedder dim).

### What it costs

Default local-st sidecar: zero per-request cost; one-time ingest of a 10 MB codebase = ~30s, ~10K rows. Gemini embedder: roughly $0.0001 per ingest of the same repo, $0.00001 per query. The savings on input tokens to downstream LLMs are ~10-50x larger than this in practical use.

## Context Compression

A content-aware compressor that runs inside the graphify pre-stage. Instead of one lexical pass over everything, it detects each prompt segment's type and applies the right compressor. Measured on a mixed corpus (tiktoken cl100k): **~36% lossless, up to ~65% on JSON/code-heavy bulk, prose ~30%→~50%** with the learned compressor. Clean-room reimplementation of [headroom](https://github.com/chopratejas/headroom) ideas (Apache-2.0); see `NOTICE`.

Two tiers:

- **Always-on lossless** (`always_structural`, default on) — JSON whitespace compaction + prose whitespace; keeps comments, never substitutes content. Runs on all traffic.
- **Aggressive, opt-in** — per request via `X-Kronaxis-Graphify: compress`. Adds: JSON null/empty pruning + array-of-objects **tabularisation** (`{"__cols__":[…],"__rows__":[[…]]}`), string-literal-aware **code comment stripping** (safe languages only — bash/yaml excluded), and a learned **LLMLingua-2 prose compressor** (self-hosted GPU sidecar, see `services/prose-compressor/`).

**CCR (reversible elision):** oversized segments can be stashed and replaced with a stub the model expands on demand via the `compress_retrieve` MCP tool or `GET /v1/compress/retrieve?id=<id>`. Elision only happens for clients that can fetch it back — a request with `X-Kronaxis-Compress-CCR: 1` or an allowlisted `X-Kronaxis-Service` — so content is never dropped from a client that can't retrieve it.

```yaml
graphify:
  enabled: true
  always_structural: true        # lossless tier on all traffic (default true)
  json_tabularize: true          # hoist repeated keys in arrays of objects
  json_drop_nulls: false         # prune null/empty fields (lossy)
  ccr_enabled: false             # reversible compress-cache-retrieve
  ccr_threshold_chars: 4000
  ccr_services: []               # services allowed to receive CCR stubs
  prose_compressor:
    enabled: false               # learned LLMLingua-2 endpoint (lossy)
    url: "http://gpu-host:8056/compress"
    rate: 0.5                     # fraction of prose tokens to keep
    min_chars: 600
    timeout_ms: 8000
```

Response headers: `X-Kronaxis-Graphify` (mode applied: `lossless`/`compress`/`augment`/`off`), `X-Kronaxis-Graphify-Tokens-Saved`, `X-Kronaxis-Graphify-Chunks`. If the prose endpoint is down or slow, the router silently falls back to the lexical result — a request never fails because compression is unavailable.

## Production Safety & Intelligence

Shipped in v0.3.0. All off by default; enable per need.

- **Schema-validated quality gates** — validate the cheap model's JSON output against a JSON Schema; on violation, silently retry on the fallback (expensive) backend so the client always gets schema-valid JSON. Enable with `QUALITY_GATE_ENABLED=true` (`QUALITY_GATE_MODE`, `QUALITY_GATE_FALLBACK`).
- **Anthropic cache breakpoints** — set `cache_breakpoints: true` on a backend to inject `cache_control: {"type":"ephemeral"}` markers on the stable prefix. Stacks multiplicatively with sessions for provider-side cache hits.
- **Shadow routing** — mirror a configurable % of traffic to a candidate backend and compare outputs (Jaccard similarity), without returning the shadow response. Configured via `ab_tests` (`variant_a`/`variant_b`/`split_pct`/`mode: shadow`); results at `GET /api/shadow/stats`. Answers "if we switched to model X, what would we save and how similar are the answers?"
- **Cost forecasting** — linear burn-rate extrapolation per service ("`my-api` hits its $50 budget at 2:14 PM"). `GET /api/costs/forecast`.
- **DPO dataset export** — every quality-gate fallback (cheap fails, expensive succeeds) is logged as a preference pair (rejected/chosen) to build a fine-tuning dataset. Enable with `DPO_EXPORT_PATH=...`; inspect via `GET /api/dpo`.

## Agent Gateway

Optional sub-service at `agent-gateway/`. Exposes CLI agents as OpenAI-compatible endpoints, so any kronaxis service that already speaks OpenAI can talk to a real agentic loop without changing client code.

### Why it's separate

Stateless LLM proxying (kronaxis-router's main job) and agentic-loop orchestration are different problems with different lifecycles -- one is request/response, the other holds workspaces, spawns subprocesses, manages tool surfaces. The gateway is its own Go module so the router stays focused on routing.

### Adapters

| Model id | Adapter | What it does |
|---|---|---|
| `claude-code-agent` | claude-cli | Spawns the `claude` CLI in stream-json mode with the full skill/MCP/hook surface, in an isolated git worktree. Returns SSE plus a `git diff` of files the agent touched. |
| `claude-sdk-agent` | anthropic-sdk | Direct call to api.anthropic.com `/v1/messages` for cheap stateless inference. Auth via `ANTHROPIC_API_KEY`. |
| `gemini-cli-agent` | gemini-cli | Spawns the `gemini` CLI in non-interactive mode. Streams stdout. |

Adding a new CLI = one Go file. The `AgentAdapter` interface is the contract.

### Per-request features

- **Streaming SSE** with proper OpenAI `tool_calls` deltas (id + function.name + arguments accumulation), keepalive heartbeats every 15 s.
- **Pass-through Claude flags** via non-standard request fields: `system_prompt`, `agent`, `permission_mode`, `claude_model`, `effort`, `allowed_tools`, `disallowed_tools`, `mcp_config`, `add_dirs`, `bare`, `include_hook_events`.
- **Persistent named workspaces** via `POST /v1/workspaces`. Pass `workspace_id` in subsequent calls for multi-turn against a stable repo.
- **Skill routing** via `model: "claude-code-agent+brainstorming"` -- prefixes the first user message with `/skillname`.
- **Multi-account auth pool**: round-robin across configured accounts, pin via `account_id` field, auto-disable on rate-limit / auth / credit / transient errors with provider-aware cooldowns. `GET /v1/accounts` for state.

### Operational

- Warm pool (configurable, default 2) pre-creates worktrees so cold-start drops from ~3 s to ~0.3 s.
- TTL sweeper reaps idle workspaces.
- JSON audit log per request (file or stderr) including model, adapter, status, num_turns, cost_usd, duration, account_id.
- Prometheus metrics at `/metrics` plus a live UI dashboard at `/`.
- systemd unit shipped (`agent-gateway/agent-gateway.service`).

### Run it

```bash
cd agent-gateway
go build -o agent-gateway .
./agent-gateway -config config.yaml
```

Then point kronaxis-router at it as a regular `type: openai` backend. There's a commented sample stanza in `config.yaml` near the OpenAI examples; uncomment to wire it in.

Full docs: [`agent-gateway/README.md`](agent-gateway/README.md).

## Docker

```yaml
# docker-compose.yml
services:
  kronaxis-router:
    build: ./kronaxis-router
    ports:
      - "8050:8050"
    volumes:
      - ./config.yaml:/app/config.yaml
    environment:
      - GEMINI_API_KEY=${GEMINI_API_KEY}
      - DATABASE_URL=postgres://user:pass@db:5432/mydb?sslmode=disable
```

## LoRA Adapter Routing

If your vLLM instance serves multiple LoRA adapters, list them in the backend config:

```yaml
backends:
  - name: my-vllm
    url: "http://localhost:8000"
    type: vllm
    lora_adapters: [default, sdr, closer, researcher]
```

Set the `model` field in the OpenAI request to the adapter name. The router will automatically route to a backend that has it loaded:

```bash
curl http://localhost:8050/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "sdr", "messages": [{"role": "user", "content": "Draft cold outreach..."}]}'
```

If no backend has the requested adapter, the router falls back to any available backend (system prompt provides role context instead of LoRA).

## Batch API Workflow (50% Off)

For non-time-sensitive work, submit to the async batch API. Most providers offer 50% off standard pricing.

**Submit a batch:**
```bash
curl -X POST http://localhost:8050/api/batch/submit \
  -H "Content-Type: application/json" \
  -d '{
    "backend": "cloud-fast",
    "callback_url": "https://my-app.com/webhook",
    "requests": [
      {"custom_id": "req-1", "body": {"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Summarise..."}], "max_tokens": 200}},
      {"custom_id": "req-2", "body": {"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "Classify..."}], "max_tokens": 50}}
    ]
  }'
```

**Poll for status:**
```bash
curl http://localhost:8050/api/batch?id=batch_1234567890
```

**Stream updates (SSE):**
```bash
curl http://localhost:8050/api/batch/stream?id=batch_1234567890
```

**Get results:**
```bash
curl http://localhost:8050/api/batch/results?id=batch_1234567890
```

Results are also delivered via webhook if `callback_url` was set. Supported providers: OpenAI, Anthropic, Gemini, Mistral, Groq, Together AI, Fireworks AI.

Requests with `X-Kronaxis-Priority: bulk` are **automatically** submitted to the batch API when the backend supports it, returning a job ID instead of blocking.

## Streaming

Streaming (`"stream": true`) is supported for vLLM and OpenAI-compatible backends. The router proxies SSE chunks in real time with `<think>` tag stripping.

For Gemini and Ollama backends, streaming requests fall back to a non-streaming response (these providers use different streaming protocols).

Streaming responses bypass batching and caching.

## Health Checks

The router probes each backend every 30 seconds (configurable):
- **vLLM/Ollama**: GET to the configured `health_endpoint` (default `/v1/models`)
- **Cloud APIs**: tracked via actual request success/failure (no probe needed)

Status transitions: healthy -> degraded (1 failure) -> down (3+ failures) -> healthy (1 success). Backends marked `down` are skipped during routing.

Actual request errors from any backend (including cloud) also update the health status.

## Monitoring with Prometheus

Scrape the `/metrics` endpoint with Prometheus:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: kronaxis-router
    static_configs:
      - targets: ['localhost:8050']
```

Available metrics:
- `kronaxis_router_requests_total{service,backend,rule}` -- request counter
- `kronaxis_router_errors_total{service,backend,rule}` -- error counter (4xx/5xx)
- `kronaxis_router_request_duration_ms_bucket{le}` -- latency histogram
- `kronaxis_router_cache_hits_total` / `kronaxis_router_cache_misses_total`
- `kronaxis_router_batch_submitted_total` / `kronaxis_router_batch_completed_total`
- `kronaxis_router_backend_healthy{backend,type}` -- 1=healthy, 0=down
- `kronaxis_router_backend_active_requests{backend,type}` -- in-flight count
- `kronaxis_router_uptime_seconds`

## Performance

Benchmarked with a mock backend (instant responses) to isolate router overhead. All tests on a standard Linux server.

### Throughput

| Concurrent Connections | Requests/sec | Avg Latency |
|----------------------|-------------|-------------|
| 50 | 15,890 | 1.7ms |
| 200 | 21,738 | 5.4ms |
| 500 | 22,770 | 20ms |

For comparison, a typical vLLM instance serves 50-200 req/s depending on model size and GPU. The router will never be the bottleneck.

### Latency Distribution (200 concurrent, 10K requests)

| Percentile | Latency |
|------------|---------|
| P10 | 0.6ms |
| P50 | 5.4ms |
| P90 | 21ms |
| P99 | 42ms |

A real LLM call takes 500ms-30s. The router adds 2-5ms median. That is 0.01-1% of total request time.

### Resource Usage

| Metric | Value |
|--------|-------|
| Binary size | 9.9 MB |
| Memory (idle) | 2.1 MB |
| Memory (500 concurrent, 50K requests) | 2.1 MB |
| CPU (idle) | 0% |

2.1 MB RSS under full load. Go's runtime does not allocate for proxy traffic because request bodies are streamed, not buffered.

### Routing Accuracy

Evaluated against 25 labelled prompts (15 extraction, 10 reasoning):

| Category | Accuracy | Detail |
|----------|----------|--------|
| Extraction (tier 2, cheap model) | 15/15 (100%) | Every extraction task correctly routed to cheap model |
| Reasoning (tier 1, powerful model) | 10/10 (100%) | Zero quality risks: no reasoning task sent to cheap model |
| Quality risks | 0 | The classifier never sends a hard task to a cheap model |
| Cost savings captured | 100% | Every extraction task gets the cost reduction |

The classifier is deliberately conservative: when uncertain, it routes to the more capable (expensive) model. This means some requests that could have been handled cheaply get sent to the expensive model (wasted money), but no request that needs the expensive model gets sent to the cheap one (no quality degradation). The cost of a false negative (missed saving) is dollars. The cost of a false positive (bad output) is trust.

## Performance Tuning

| Setting | Default | Guidance |
|---------|---------|----------|
| `max_concurrent` per backend | 10 | Match your GPU's max concurrent requests (vLLM: check `--max-num-seqs`) |
| `batching.window_ms` | 50 | Lower = less latency, higher = better GPU utilisation. Only affects background/bulk. |
| `batching.max_batch_size` | 8 | Match vLLM's batch size. Larger = fewer HTTP calls but more memory. |
| `CACHE_MAX_SIZE` | 1000 | Increase for repeated prompts (e.g. classification pipelines). Each entry is ~1-10KB. |
| `CACHE_TTL_SECONDS` | 3600 | Lower for frequently changing data. 0 = disabled. |
| Rate limits | None | Set per-service to prevent a runaway job from starving interactive traffic. |

## Troubleshooting

**All requests return 503:** No healthy backends. Check `/health` -- are backends reachable? Check URLs, firewalls, and that vLLM is actually running.

**Requests are slow but succeed:** Check if batching is adding latency to non-bulk requests. Set `batching.enabled: false` or ensure your priority is `normal` (not `background`).

**Budget rejected (429):** Daily cost limit exceeded. Check `/api/costs` to see breakdown. Increase the limit or set `action: downgrade` instead of `reject`.

**Cache never hits:** Only temperature=0 requests are cached. Streaming requests are never cached. Check `CACHE_MAX_SIZE > 0`.

**LoRA adapter not found:** The router routes to any healthy backend if no backend has the adapter. Check your backend config lists the adapter in `lora_adapters`.

**Gemini returns 403/429:** API key invalid or rate limited. The router passes 4xx errors through to the caller. Check your key and Gemini quota.

## Roadmap

See [ROADMAP.md](ROADMAP.md) for where the project is heading: KV cache-aware routing, stateful session management, queue-aware load balancing, schema-validated quality gates, and more. Implementation details in [docs/IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md).

## Further Reading

- [Stop Paying Frontier Prices for Tasks a Local Model Handles Fine](https://kronaxis.co.uk/blog/llm-routing-cost-savings) -- full blog post with cost arithmetic, quality validation, and comparison to LiteLLM, OpenRouter, Portkey, and Martian

## Licence

Business Source License 1.1. Source-available; non-commercial use is permitted under the Additional Use Grant. The Licensed Work converts to Apache License, Version 2.0 on 9 May 2031 (the Change Date). Commercial production use before that date requires a commercial licence -- contact `contact@kronaxis.co.uk`. See [LICENSE](LICENSE) for the full terms.

Built by [Kronaxis](https://kronaxis.co.uk).
