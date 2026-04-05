# Kronaxis Router

Intelligent LLM proxy that routes requests to the cheapest model capable of delivering the required output quality.

A CFO can fill in accounts receivable, but a bookkeeper is 50x cheaper and does the job just as well. Kronaxis Router applies this principle to LLM inference: structured extraction goes to the small model, heavy reasoning goes to the large model, and bulk work goes to whatever is cheapest and available.

## Features

- **Cost-optimised routing** -- YAML rules match on task type, service, tier, priority, and content type. Route to the cheapest capable backend.
- **Multi-backend support** -- Local vLLM, Gemini, OpenAI, Ollama. Mix local GPUs with cloud APIs. Automatic format adaptation.
- **LoRA adapter routing** -- Knows which vLLM instances have which adapters loaded. Routes role-specific requests to the right instance.
- **Throughput batching** -- Background/bulk requests collected over a 50ms window and dispatched as a single multi-prompt `/v1/completions` call to vLLM. Improves GPU utilisation on self-hosted models.
- **Cost-saving batch API** -- Submit bulk work to provider batch APIs (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks) for **50% off** standard pricing. Async JSONL processing, typically completes in minutes to hours. Submit via `POST /api/batch/submit`, poll via `GET /api/batch`, retrieve results via `GET /api/batch/results`.
- **Per-service budgets** -- Daily cost limits per calling service. Exceeding a budget triggers downgrade (cheaper model) or rejection.
- **Health checks & failover** -- 30-second health probes. Automatic failover through the backend preference chain.
- **Streaming pass-through** -- SSE forwarding for real-time use cases (voice, chat).
- **Qwen3 thinking mode** -- Auto-disables thinking mode and strips `<think>` tags for Qwen3/3.5 models.
- **Hot-reloadable config** -- Edit `config.yaml` and rules update within 5 seconds. No restart needed.
- **OpenAI API compatible** -- Drop-in replacement. Services change one URL.

## Quick Start

```bash
# Clone
git clone https://github.com/kronaxis/kronaxis-router.git
cd kronaxis-router

# Edit config.yaml with your backends
vim config.yaml

# Build and run
go build -o kronaxis-router .
./kronaxis-router

# Or with Docker
docker build -t kronaxis-router .
docker run -p 8050:8050 -v $(pwd)/config.yaml:/app/config.yaml kronaxis-router
```

Point your services at `http://localhost:8050/v1/chat/completions` instead of calling LLM backends directly.

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

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `CONFIG_PATH` | `config.yaml` | Path to configuration file |
| `ROUTER_PORT` | `8050` | HTTP listen port |
| `DATABASE_URL` | (empty) | PostgreSQL connection string for cost logging |
| `GEMINI_API_KEY` | (empty) | Referenced via `env:GEMINI_API_KEY` in config |

## Response Headers

Every response includes (when branding is enabled):

```
X-Powered-By: Kronaxis Router
X-Kronaxis-Router-Version: 1.0.0
X-Kronaxis-Backend: local-large
X-Kronaxis-Rule: heavy-reasoning
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

## Licence

Apache 2.0. See [LICENSE](LICENSE).

Built by [Kronaxis](https://kronaxis.co.uk).
