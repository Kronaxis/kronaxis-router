# Kronaxis Router

[![Build](https://github.com/kronaxis/kronaxis-router/actions/workflows/build.yml/badge.svg)](https://github.com/kronaxis/kronaxis-router/actions/workflows/build.yml)

Intelligent LLM proxy that routes requests to the cheapest model capable of delivering the required output quality.

A CFO can fill in accounts receivable, but a bookkeeper is 50x cheaper and does the job just as well. Kronaxis Router applies this principle to LLM inference: structured extraction goes to the small model, heavy reasoning goes to the large model, and bulk work goes to whatever is cheapest and available.

## Features

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
- **Streaming pass-through** -- SSE forwarding for real-time use cases (voice, chat).
- **Qwen3 thinking mode** -- Auto-disables thinking mode and strips `<think>` tags for Qwen3/3.5 models.
- **Hot-reloadable config** -- Edit `config.yaml` and rules update within 5 seconds. No restart needed.
- **Embedded web UI** -- Dashboard, visual flow builder, backend manager, cost analysis, config editor.
- **API authentication** -- Bearer token auth on `/api/*` endpoints via `ROUTER_API_TOKEN` env var.
- **OpenAI API compatible** -- Drop-in replacement. Services change one URL.

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
```

## Database (Optional)

If `DATABASE_URL` is set, the router logs all requests to the `llm_call_log` table for cost analysis. The router auto-creates the required `service` column on startup.

Without a database, the router works fully -- cost tracking happens in memory only and resets on restart.

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

## Further Reading

- [Stop Paying Frontier Prices for Tasks a Local Model Handles Fine](https://kronaxis.co.uk/blog/llm-routing-cost-savings) -- full blog post with cost arithmetic, quality validation, and comparison to LiteLLM, OpenRouter, Portkey, and Martian

## Licence

Apache 2.0. See [LICENSE](LICENSE).

Built by [Kronaxis](https://kronaxis.co.uk).
