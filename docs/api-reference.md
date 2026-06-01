# API Reference

All endpoints are served on the configured port (default 8050).

## Proxy Endpoint

### POST /v1/chat/completions

OpenAI-compatible chat completions proxy. This is the main endpoint.

**Request:** Standard OpenAI ChatCompletion request body.

**Routing headers** (optional):

| Header | Type | Description |
|--------|------|-------------|
| `X-Kronaxis-Service` | string | Service name for routing, budgets, rate limits. Also drives graphify mode via `service_overrides` map. |
| `X-Kronaxis-CallType` | string | Task type for rule matching |
| `X-Kronaxis-Priority` | string | `interactive`, `normal`, `background`, `bulk` |
| `X-Kronaxis-Tier` | int | `1` (heavy reasoning), `2` (structured extraction) |
| `X-Kronaxis-PersonaID` | string | Cost attribution identifier |
| `X-Kronaxis-Graphify` | string | `compress` / `augment` / `auto` / `off`. Overrides global default + service override. |
| `X-Kronaxis-Response-Schema` | string (JSON) | JSON Schema to validate the model's JSON output against; on violation the gate retries on the fallback backend (needs `QUALITY_GATE_FALLBACK`). |
| `X-Kronaxis-Compress-CCR` | `1` | Opt this client in to CCR elision (it can fetch elided blocks via `compress_retrieve`). |
| `X-Kronaxis-Session-Create` | `true` | Store this transcript and return a session id. |
| `X-Kronaxis-Session-ID` | string | Hydrate a stored session; send only the new turn. |
| `X-Kronaxis-Session-TTL` | duration | Override the session TTL on create. |
| `X-Kronaxis-Reflect` | `1` | Run a System-2 review pass on the answer before returning (non-streaming). |
| `X-Kronaxis-Consensus` | `1` | Dispatch to several backends; return the agreed answer or an arbiter's resolution. |

**Response:** Standard OpenAI ChatCompletion response.

**Additional response headers:**

| Header | Description |
|--------|-------------|
| `X-Powered-By` | `Kronaxis Router` |
| `X-Kronaxis-Router-Version` | Router version |
| `X-Kronaxis-Backend` | Backend that served the request |
| `X-Kronaxis-Rule` | Rule that matched |
| `X-Kronaxis-Cache` | `HIT` (exact) or `SEMANTIC` (fuzzy near-duplicate) if served from cache |
| `X-Kronaxis-Reflected` | `true` if a System-2 reflection pass refined the answer |
| `X-Kronaxis-Consensus` | `agreed` or `arbitrated` when consensus mode ran |
| `X-Kronaxis-Graphify` | Mode actually used (`lossless` / `compress` / `augment`; only present when it ran) |
| `X-Kronaxis-Graphify-Chunks` | Number of chunks injected |
| `X-Kronaxis-Graphify-Tokens-Saved` | Approximate input tokens saved by compression |
| `X-Kronaxis-Complexity` | Auto-classified complexity score (0–100) when tier was unset |
| `X-Kronaxis-Quality-Gate` | `retried` if the quality gate fell back to a stronger backend |
| `X-Kronaxis-Session-ID` / `X-Kronaxis-Session-Created` | Session id (and whether newly created) |

**Special behaviour for `bulk` priority:** If the target backend supports batch APIs, returns HTTP 202 with a batch job instead of a synchronous response.

---

## Graphify Pre-Stage

### POST /v1/retrieve

Raw retrieval against `kr_chunks`. Useful for debugging or for callers who want to do their own context injection.

**Request:**
```json
{
  "query": "how does the auth handler work?",
  "top_k": 5,
  "min_cosine_sim": 0.4,
  "max_chars": 3200,
  "bm25_weight": 0.3,
  "path_prefix": "/path/to/your/project"
}
```

**Response:**
```json
{
  "query": "...",
  "results": [
    {
      "id": 12345,
      "source_path": "kronaxis-router/agent-gateway/auth_pool.go",
      "chunk_idx": 3,
      "content": "[file: ...]\n...",
      "score": 0.78,
      "cosine_sim": 0.84,
      "bm25_score": 1.2,
      "metadata": {"ext": "go", "file_mtime": "2026-05-08T13:24:00Z"}
    }
  ],
  "embedder": "local-st:BAAI/bge-small-en-v1.5",
  "took": "12ms"
}
```

### GET /api/graphify

Counters + current state of the graphify middleware.

```json
{
  "enabled": true,
  "embedder": "local-st:BAAI/bge-small-en-v1.5",
  "embedder_dim": 384,
  "default_mode": "auto",
  "requests_total": 1024,
  "augments_total": 612,
  "compress_total": 184,
  "off_total": 228,
  "chunks_retrieved_total": 4126,
  "tokens_saved_total": 124000,
  "errors_total": 3
}
```

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

## Cluster Intelligence, Sessions, Compression & Ops

Endpoints added in v0.3.0 and later. See the README for behaviour and config.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/sessions` | GET | List stored sessions |
| `/v1/sessions/<id>` | GET / DELETE | Inspect / delete a session |
| `/api/kv-trees` | GET | Inspect per-backend KV-cache prefix trees (KV pinning) |
| `/v1/compress/retrieve?id=<id>` | GET | Fetch the original of a CCR-elided block (`?format=json` for metadata) |
| `/api/costs/forecast` | GET | Per-service budget burn-rate forecast |
| `/api/shadow/stats` | GET | Shadow-routing comparison stats (Jaccard similarity) |
| `/api/dpo` | GET | DPO preference-pair export status |

Queue-aware load balancing has no endpoint of its own; per-backend `queue_depth` and `active_inference` appear in `GET /api/backends` and `GET /health`. Schema-validated quality gating is driven by the `X-Kronaxis-Response-Schema` request header (see the proxy endpoint above).

The `compress_retrieve` MCP tool wraps `/v1/compress/retrieve` for MCP clients.

---

## Authentication

When `ROUTER_API_TOKEN` is set, all `/api/*` endpoints require:

```
Authorization: Bearer <token>
```

The proxy endpoint (`/v1/chat/completions`), health (`/health`), metrics (`/metrics`), and the web UI (`/`) are not auth-gated.
