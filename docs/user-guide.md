# User Guide

## What Kronaxis Router Does

Kronaxis Router sits between your applications and LLM backends. Every request that would normally go directly to OpenAI, Gemini, vLLM, or Ollama goes through the router instead. The router decides which backend to use based on rules you define, optimising for cost, quality, and availability.

**The core principle:** route every request to the cheapest model that can reliably deliver the required output.

## Quick Start

### 1. Start the router

```bash
./kronaxis-router
```

### 2. Point your app at it

Replace your LLM API URL with the router:

```
# Before
https://api.openai.com/v1/chat/completions

# After
http://localhost:8050/v1/chat/completions
```

The router is fully OpenAI API compatible. No code changes needed beyond the URL.

### 3. Add routing metadata (optional)

For smarter routing, add headers to your requests:

```bash
curl http://localhost:8050/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Kronaxis-Service: my-app" \
  -H "X-Kronaxis-Tier: 2" \
  -d '{"model":"default","messages":[{"role":"user","content":"Classify this as positive or negative: Great product!"}],"max_tokens":50}'
```

Without headers, the router uses **automatic classification** to determine the right tier.

## Routing Headers

| Header | Values | Purpose |
|--------|--------|---------|
| `X-Kronaxis-Service` | Any string | Identifies your app for cost tracking and rate limiting |
| `X-Kronaxis-Tier` | `1` (heavy), `2` (light) | Override auto-classification. Tier 1 = reasoning, Tier 2 = extraction |
| `X-Kronaxis-Priority` | `interactive`, `normal`, `background`, `bulk` | Controls batching and auto-batch routing |
| `X-Kronaxis-CallType` | Any string | Task type for fine-grained rule matching |
| `X-Kronaxis-PersonaID` | Any string | Cost attribution to a specific entity |

**All headers are optional.** The router works without any of them.

## Automatic Tier Classification

When `X-Kronaxis-Tier` is not set, the router analyses your prompt and automatically assigns a tier:

**Tier 1 (heavy reasoning)** is assigned when:
- The prompt contains planning/strategy/analysis keywords
- The prompt asks for multi-step reasoning or creative writing
- The expected output is long (max_tokens > 1000)
- Temperature is high (>= 0.8)

**Tier 2 (structured extraction)** is assigned when:
- The prompt asks for classification, scoring, or JSON output
- The system prompt requests JSON format
- The prompt is short with a specific expected format
- Temperature is 0 (deterministic)

If the classifier cannot determine the tier, it defaults to your fallback chain.

## Priority Levels

| Priority | Batching | Auto-Batch | Use Case |
|----------|----------|------------|----------|
| `interactive` | Bypassed (0ms) | Never | Phone calls, live chat, copilot |
| `normal` | Bypassed (0ms) | Never | API responses, UI interactions |
| `background` | 50ms window | Never | Enrichment, scheduled tasks |
| `bulk` | 50ms window | **Yes (50% off)** | Training data, batch processing |

`bulk` priority requests are automatically submitted to the provider's batch API (OpenAI, Gemini, Anthropic, etc.) for 50% cost savings. Instead of a synchronous response, you receive a job ID:

```json
{
  "batch": true,
  "job_id": "batch_1712345678",
  "message": "Request submitted to async batch API for 50% cost savings.",
  "status": "submitted"
}
```

## LoRA Adapter Routing

If your vLLM instance serves multiple LoRA adapters, set the `model` field to the adapter name:

```json
{"model": "sdr", "messages": [...]}
```

The router finds a backend with the `sdr` adapter loaded and routes to it. If no backend has the adapter, it falls back to the base model (the system prompt provides role context instead).

Configure adapters in `config.yaml`:

```yaml
backends:
  - name: my-vllm
    url: "http://localhost:8000"
    type: vllm
    lora_adapters: [default, sdr, closer, researcher]
```

## Failover

When a backend fails (5xx error, timeout, connection refused), the router automatically tries the next backend in the rule's preference list:

```
Rule: [local-gpu, cloud-fast, cloud-powerful]

1. Try local-gpu       -> 503 (GPU busy)
2. Try cloud-fast      -> 200 (success) -> return to caller
```

Each backend gets one retry with 500ms backoff before the router moves to the next candidate. Transport errors (connection refused, DNS failure) and 5xx responses trigger failover. 4xx responses (rate limits, auth errors) are passed through to the caller.

## Response Caching

Identical requests with `temperature: 0` are cached. The second time the same prompt is sent with the same model and parameters, the response is served from cache without calling any backend.

Cache behaviour:
- Only `temperature: 0` requests are cached (deterministic output)
- Streaming requests are never cached
- Cache key includes: model, messages, max_tokens, top_p, n
- Default: 1000 entries, 1 hour TTL
- Response header `X-Kronaxis-Cache: HIT` indicates a cache hit

Configure via environment variables:
```
CACHE_MAX_SIZE=1000      # 0 to disable
CACHE_TTL_SECONDS=3600   # seconds
```

## Cost Budgets

Set daily spending limits per service:

```yaml
budgets:
  my-api:
    daily_limit_usd: 50.00
    action: downgrade          # or "reject"
    downgrade_target: local-small
```

When budget is exceeded:
- `downgrade`: requests route to a cheaper backend (cost-saving, no errors)
- `reject`: requests return HTTP 429 (hard stop)

Check current spend: `GET /api/costs`

## Rate Limiting

Protect backends from traffic spikes:

```yaml
rate_limits:
  my-api:
    requests_per_second: 100
    burst_size: 200
```

Rate-limited requests receive HTTP 429 with a `Retry-After: 1` header.

## Batch API (50% Off)

For non-time-sensitive work, submit batches to provider APIs:

```bash
# Submit
curl -X POST http://localhost:8050/api/batch/submit \
  -H "Content-Type: application/json" \
  -d '{
    "backend": "cloud-fast",
    "callback_url": "https://my-app.com/webhook",
    "requests": [
      {"custom_id": "1", "body": {"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "..."}]}},
      {"custom_id": "2", "body": {"model": "gemini-2.5-flash", "messages": [{"role": "user", "content": "..."}]}}
    ]
  }'

# Poll status
curl http://localhost:8050/api/batch?id=batch_xxx

# Get results (when status=completed)
curl http://localhost:8050/api/batch/results?id=batch_xxx
```

Results can be delivered three ways:
1. **Webhook**: POST to your `callback_url` when complete
2. **SSE stream**: `GET /api/batch/stream?id=xxx`
3. **Polling**: check status, then fetch results

Supported providers: OpenAI (50%), Anthropic (50%), Gemini (50%), Mistral (50%), Groq (50%), Together AI (50% on select models), Fireworks AI (50%).

## Web UI

Access the dashboard at http://localhost:8050:

- **Dashboard**: live metrics, backend health, cost tracking
- **Flow Builder**: visual drag-and-drop rule editor
- **Backends**: add/remove/monitor backends
- **Costs**: breakdown by service, model, call type
- **Config**: edit YAML directly, save and apply

## Quality Validation

The router can sample responses from cheap models and validate them against a reference (expensive) model:

```
QUALITY_ENABLED=true
```

When enabled (5% sample rate by default), the router:
1. Sends the same prompt to the reference model
2. Compares outputs using word-overlap similarity
3. If cheap model quality drops below threshold: auto-promotes that task type to the next tier
4. If quality recovers: auto-demotes back to the cheap model

This creates a closed-loop cost optimisation: the router proves the cheap model works before trusting it.

## A/B Testing

Split traffic between models to compare quality and cost:

```yaml
ab_tests:
  - name: gemini-vs-local
    match: {service: my-api}
    variant_a: local-large
    variant_b: gemini-flash
    split_pct: 10     # 10% to variant B
    active: true
```

View results: `GET /api/abtests`

## Streaming

Set `"stream": true` in your request. The router proxies SSE chunks in real time for vLLM and OpenAI-compatible backends. Gemini and Ollama backends fall back to a complete response (they use different streaming protocols).

`<think>` tags from Qwen3/3.5 models are automatically stripped from streaming output.

## Next Steps

- [Configuration Reference](configuration.md)
- [API Reference](api-reference.md)
- [Deployment Guide](deployment.md)
- [Monitoring Guide](monitoring.md)
