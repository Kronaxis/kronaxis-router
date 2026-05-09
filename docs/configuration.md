# Configuration Reference

Configuration is loaded from `config.yaml` (override with `CONFIG_PATH` env var). Changes are detected and applied automatically every 5 seconds.

## Full Schema

```yaml
server:
  port: 8050                          # HTTP listen port
  health_check_interval: 30s          # Backend health probe interval
  default_timeout: 120s               # Default HTTP timeout
  branding:
    headers: true                     # Add X-Powered-By headers
    header_name: "Kronaxis Router"    # Branding name
    content_inject: false             # Inject branding into response text
    content_text: "\n\n---\n..."      # Branding text (when content_inject=true)
    content_skip_json: true           # Never inject into JSON responses

backends:
  - name: my-backend                  # Unique identifier (required)
    url: "http://localhost:8000"      # Base URL (required)
    type: vllm                        # vllm | gemini | openai | ollama
    model_name: "my-model"            # Model name sent to backend
    cost_input_1m: 0.01               # USD per 1M input tokens
    cost_output_1m: 0.01              # USD per 1M output tokens
    capabilities:                     # Capability tags for rule matching
      - json_output
      - long_context
      - vision
      - lora_adapter
    max_concurrent: 10                # Max simultaneous requests
    lora_adapters:                    # LoRA adapter names loaded on this backend
      - default
      - sdr
    api_key: "env:GEMINI_API_KEY"     # API key (supports env: prefix)
    dynamic: false                    # true = registered via API, survives reloads
    health_endpoint: "/v1/models"     # Health check path

rules:
  - name: my-rule                     # Unique name (required)
    priority: 100                     # Higher = evaluated first
    match:
      service: ""                     # Match X-Kronaxis-Service (empty=any)
      call_type: ""                   # Match X-Kronaxis-CallType
      tier: 0                         # Match X-Kronaxis-Tier (0=any)
      model: ""                       # Match request model field
      lora: ""                        # Match LoRA adapter name
      priority_level: ""              # Match X-Kronaxis-Priority
      content_type: ""                # Match detected content type (text/vision)
    backends:                         # Ordered backend preference list
      - cheap-backend
      - expensive-backend
    max_cost_1m: 0.50                 # Cost ceiling (0=unlimited)
    required_capabilities:            # All must be present on the backend
      - vision

defaults:
  fallback_chain:                     # Used when no rule matches
    - cheap
    - expensive
  default_timeout_ms: 120000          # Timeout for forwarded requests

budgets:
  my-service:
    daily_limit_usd: 50.00            # Daily spend cap
    action: downgrade                 # downgrade | reject
    downgrade_target: cheap-backend   # Backend for downgraded requests
  default:                            # Fallback for unlisted services
    daily_limit_usd: 100.00
    action: downgrade
    downgrade_target: cheap-backend

rate_limits:
  my-service:
    requests_per_second: 100          # Sustained rate
    burst_size: 200                   # Max burst
  default:
    requests_per_second: 500
    burst_size: 1000

batching:
  enabled: true                       # Enable throughput batching
  window_ms: 50                       # Collection window (ms)
  max_batch_size: 8                   # Max requests per batch
  priority_bypass:                    # Priorities that skip batching
    - interactive
```

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `CONFIG_PATH` | `config.yaml` | Config file path |
| `ROUTER_PORT` | `8050` | HTTP listen port |
| `DATABASE_URL` | (empty) | PostgreSQL for cost logging |
| `ROUTER_API_TOKEN` | (empty) | Bearer token for `/api/*` auth |
| `ROUTER_ALLOW_PRIVATE_BACKENDS` | (empty) | Set `true` to allow private IP backends |
| `CACHE_MAX_SIZE` | `1000` | Max cached responses (0=disabled) |
| `CACHE_TTL_SECONDS` | `3600` | Cache entry TTL |
| `BATCH_DATA_DIR` | `/tmp/kronaxis-router-batches` | Batch job storage |
| `QUALITY_ENABLED` | (empty) | Set `true` for quality validation |
| `AUDIT_ENABLED` | (empty) | Set `true` for audit logging |
| `AUDIT_LOG_FILE` | `audit.jsonl` | Audit log path |
| `AUDIT_MAX_ENTRIES` | `100000` | Rotate after N entries |
| `GEMINI_API_KEY` | (empty) | Referenced via `env:GEMINI_API_KEY` |

## Environment Variable References in Config

Backend fields that support `env:` prefix resolution:
- `api_key: "env:MY_API_KEY"` resolves to the value of `$MY_API_KEY`
- `url: "env:MY_BACKEND_URL"` resolves to the value of `$MY_BACKEND_URL`

## Backend Types

| Type | Protocol | Health Check | Streaming | Batch API |
|------|----------|-------------|-----------|-----------|
| `vllm` | OpenAI-compatible | `/v1/models` | SSE | Multi-prompt `/v1/completions` |
| `openai` | OpenAI | (traffic-based) | SSE | File upload + `/v1/batches` |
| `gemini` | Gemini REST | (traffic-based) | Non-streaming fallback | Inline/file batch |
| `ollama` | Ollama REST | `/health` | Non-streaming fallback | None |

## Rule Matching

Rules are evaluated in **priority order** (highest first). Only non-empty/non-zero match fields must match. An empty match (`match: {}`) matches everything.

Multiple rules can have the same priority. They are evaluated in config order within the same priority level.

## Example Configs

See `examples/` directory:
- `vllm-only.yaml` -- Single local GPU, no cloud
- `cloud-only.yaml` -- Gemini + OpenAI, no GPU
- `hybrid.yaml` -- Local GPUs with cloud failover
