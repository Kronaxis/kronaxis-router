# API Reference

All endpoints are served on the configured port (default 8050).

## Proxy Endpoint

### POST /v1/chat/completions

OpenAI-compatible chat completions proxy. This is the main endpoint.

**Request:** Standard OpenAI ChatCompletion request body.

**Routing headers** (optional):

| Header | Type | Description |
|--------|------|-------------|
| `X-Kronaxis-Service` | string | Service name for routing, budgets, rate limits |
| `X-Kronaxis-CallType` | string | Task type for rule matching |
| `X-Kronaxis-Priority` | string | `interactive`, `normal`, `background`, `bulk` |
| `X-Kronaxis-Tier` | int | `1` (heavy reasoning), `2` (structured extraction) |
| `X-Kronaxis-PersonaID` | string | Cost attribution identifier |

**Response:** Standard OpenAI ChatCompletion response.

**Additional response headers:**

| Header | Description |
|--------|-------------|
| `X-Powered-By` | `Kronaxis Router` |
| `X-Kronaxis-Router-Version` | Router version |
| `X-Kronaxis-Backend` | Backend that served the request |
| `X-Kronaxis-Rule` | Rule that matched |
| `X-Kronaxis-Cache` | `HIT` if served from cache |

**Special behaviour for `bulk` priority:** If the target backend supports batch APIs, returns HTTP 202 with a batch job instead of a synchronous response.

---

## Health & Monitoring

### GET /health

Router health status with backend details.

```json
{
  "status": "ok",
  "service": "kronaxis-router",
  "version": "1.0.0",
  "uptime_seconds": 3600,
  "backends_total": 4,
  "backends_healthy": 3,
  "backends": [...],
  "cache": {"enabled": true, "size": 42, "hits": 100, "misses": 50, "hit_rate": 66.7},
  "quality": {"enabled": true, "checked": 10, "passed": 9, "failed": 1}
}
```

### GET /metrics

Prometheus-compatible metrics in text format.

### GET /api/stats

Live request statistics (JSON).

```json
{
  "total_requests": 1234,
  "active_requests": 5,
  "total_errors": 12,
  "avg_latency_ms": 150.5,
  "requests_by_rule": {"heavy-reasoning": 500, "extraction": 734},
  "requests_by_service": {"my-api": 1000, "batch-worker": 234},
  "requests_by_model": {"local-large": 800, "gemini-flash": 434}
}
```

---

## Cost Management

### GET /api/costs

Cost dashboard with breakdown.

**Query params:** `period` = `today` | `week` | `month`

```json
{
  "date": "2026-04-06",
  "daily": {"my-api": 12.50, "batch-worker": 3.20},
  "budgets": {"my-api": {"daily_limit_usd": 50, "action": "downgrade"}},
  "breakdown": [
    {"service": "my-api", "model": "local-large", "call_type": "summarise",
     "request_count": 500, "total_input_tokens": 100000, "total_output_tokens": 50000,
     "total_cost_usd": 0.0015, "avg_latency_ms": 200}
  ]
}
```

### GET /api/budgets

Current budget configuration (JSON).

### PUT /api/budgets

Update budgets. Body: `{"service": {"daily_limit_usd": 50, "action": "downgrade", "downgrade_target": "cheap"}}`.

---

## Backend Management

### GET /api/backends

List all backends with health status.

### POST /api/backends

Register a dynamic backend. Body: `BackendConfig` JSON.

```json
{
  "name": "my-new-backend",
  "url": "http://10.0.0.5:8000",
  "type": "vllm",
  "model_name": "my-model",
  "cost_input_1m": 0.01,
  "cost_output_1m": 0.01,
  "capabilities": ["json_output"],
  "max_concurrent": 10
}
```

**Note:** URLs targeting private networks require `ROUTER_ALLOW_PRIVATE_BACKENDS=true`.

### DELETE /api/backends?name=xxx

Remove a dynamic backend.

---

## Routing Rules

### GET /api/rules

List all routing rules (JSON array).

### POST /api/rules

Add a new rule. Returns 409 if name already exists.

### PUT /api/rules

Update an existing rule (matched by name).

### DELETE /api/rules?name=xxx

Delete a rule.

---

## Batch API

### POST /api/batch/submit

Submit an async batch job.

```json
{
  "backend": "cloud-fast",
  "callback_url": "https://my-app.com/webhook",
  "requests": [
    {"custom_id": "req-1", "body": {"model": "gemini-2.5-flash", "messages": [...], "max_tokens": 100}},
    {"custom_id": "req-2", "body": {"model": "gemini-2.5-flash", "messages": [...], "max_tokens": 100}}
  ]
}
```

**Response (201):**
```json
{"id": "batch_1712345678", "status": "submitted", "request_count": 2}
```

### GET /api/batch

List all batch jobs, or get status with `?id=batch_xxx`.

### GET /api/batch/results?id=batch_xxx

Retrieve results of a completed batch job (JSON array).

### GET /api/batch/stream?id=batch_xxx

SSE event stream for batch job status updates. Events: `status`, `results`, `done`.

---

## Configuration

### GET /api/config

Summary of current configuration.

### GET /api/config/yaml

Raw YAML configuration file.

### PUT /api/config/yaml

Replace the entire config file. Body: raw YAML text. Validates before saving.

### POST /api/config/reload

Force reload config from disk.

---

## A/B Testing

### GET /api/abtests

View A/B test results.

```json
[{
  "name": "gemini-vs-local",
  "variant_a": {"backend": "local-large", "requests": 900, "avg_latency_ms": 150, "total_cost": 0.009},
  "variant_b": {"backend": "gemini-flash", "requests": 100, "avg_latency_ms": 300, "total_cost": 0.060}
}]
```

---

## Authentication

When `ROUTER_API_TOKEN` is set, all `/api/*` endpoints require:

```
Authorization: Bearer <token>
```

The proxy endpoint (`/v1/chat/completions`), health (`/health`), metrics (`/metrics`), and the web UI (`/`) are not auth-gated.
