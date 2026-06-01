# Architecture

## Overview

```
                ┌─────────────────────────────────────────────────────┐
                │             Kronaxis Router  :8050                 │
   Requests ──>│                                                     │
                │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────┐│──> Local vLLM
                │  │ Cache    │  │ Graphify │  │Classifier│  │     ││──> Vast.ai
                │  │ check    │─>│pre-stage │─>│auto-tier │─>│Rules││──> Gemini
                │  └──────────┘  └──────────┘  └──────────┘  └─────┘│──> OpenAI
                │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────┐│──> Ollama
                │  │ Budget   │  │ Batcher  │  │ Quality  │  │A/B  ││──> agent-gateway:8055
                │  │ check    │  │throughput│  │Validator │  │Tests││──> embedding-svc:8053
                │  └──────────┘  └──────────┘  └──────────┘  └─────┘│
                │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────┐│
                │  │ Metrics  │  │  Audit   │  │  Batch   │  │ UI  ││
                │  └──────────┘  └──────────┘  └──────────┘  └─────┘│
                └─────────────────────────────────────────────────────┘
                                          │                  │
                                          │ pgvector         │ HTTP (/embed)
                                          ▼                  ▼
                                    ┌──────────┐       ┌──────────────┐
                                    │kr_chunks │       │embedding-svc │
                                    │(HNSW+GIN)│       │bge-small/etc │
                                    └──────────┘       └──────────────┘

           agent-gateway sub-service (separate Go module, port 8055):

                ┌──────────────────────────────────────────────────┐
                │  /v1/chat/completions  (OpenAI-compatible)       │
                │  /v1/workspaces        (multi-turn lifecycle)    │
                │  /v1/accounts          (auth-pool state)         │
                │  ↓                                                │
                │  AgentAdapter interface                           │
                │  ├ claude-cli      → spawns `claude -p`          │
                │  ├ anthropic-sdk   → POST api.anthropic.com      │
                │  └ gemini-cli      → spawns `gemini --prompt`    │
                │  ↓                                                │
                │  ephemeral git worktree   per-request CLAUDE_CONFIG_DIR
                │  + warm pool              + multi-account auth pool
                │  + TTL sweeper            + audit JSON per request
                └──────────────────────────────────────────────────┘
```

## Request Flow

Steps marked **NEW** are the graphify pre-stage added 2026-05-08.

1. **Receive** -- OpenAI-compatible POST to `/v1/chat/completions`
2. **Extract** -- Parse request body, extract `X-Kronaxis-*` headers
3. **Cache check** -- Return cached response for deterministic requests (cache.go)
4. **Graphify pre-stage** **(NEW)** -- If enabled, augment thin prompts with retrieved chunks or compress fat ones (graphify_middleware.go). Mode comes from `X-Kronaxis-Graphify` header, then `X-Kronaxis-Service` override map, then global default. Skipped silently on retrieval errors -- never fails a request.
5. **Classify** -- Auto-assign tier if not explicitly set (classifier.go)
6. **Budget check** -- Reject or prepare downgrade if budget exceeded (costs.go)
7. **Auto-batch** -- For `bulk` priority on batch-capable backends, submit async (batch.go)
8. **Route** -- Evaluate rules in priority order, get candidate backends (router.go)
9. **Budget downgrade** -- Prepend cheaper backend if over budget
10. **A/B test** -- Override backend if A/B test applies (abtest.go)
11. **Adapt** -- Inject Qwen thinking mode disable, set model name (qwen.go)
12. **Dispatch** -- Forward to first healthy candidate (proxy.go). One candidate may be agent-gateway, which kicks off an agentic loop.
13. **Failover** -- On 5xx/error, try next candidate. Retry once with 500ms backoff.
14. **Post-process** -- Strip think tags, inject branding, compress response
15. **Cache store** -- Cache deterministic successful responses
16. **Quality sample** -- Randomly validate cheap-model output against reference
17. **Log** -- Record to stats, Prometheus metrics, cost tracker, audit log
18. **Return** -- Send response to caller with branding headers (including `X-Kronaxis-Graphify`, `X-Kronaxis-Graphify-Chunks`, `X-Kronaxis-Graphify-Tokens-Saved` when graphify ran)

## File Structure

| File | Lines | Purpose |
|------|-------|---------|
| `main.go` | 282 | Entry point, HTTP server, route registration, graceful shutdown |
| `config.go` | 259 | YAML parsing, hot-reload, env var resolution, defaults |
| `router.go` | 224 | Rule matching, backend selection, candidate ordering |
| `classifier.go` | 139 | Automatic tier classification from prompt analysis |
| `proxy.go` | 943 | HTTP proxy, failover, streaming, format adaptation (Gemini/Ollama) |
| `backends.go` | 324 | Backend pool, health checks, concurrency tracking |
| `throughput.go` | 309 | Multi-prompt batching for vLLM backends |
| `batch.go` | 874 | Async batch API (7 providers), polling, webhook delivery |
| `cache.go` | 183 | Response caching (SHA-256 key, LRU eviction) |
| `costs.go` | 320 | Cost tracking, budgets, DB logging, cost dashboard |
| `quality.go` | 239 | Quality validation sampling, auto-promote/demote |
| `abtest.go` | 158 | A/B traffic splitting, per-variant metrics |
| `tokens.go` | 98 | BPE-approximation token counting |
| `compress.go` | ~330 | Content-aware compression router (JSON/code/prose dispatch, lossless + aggressive profiles) |
| `compress_json.go` | ~190 | JSON compaction, null-pruning, array-of-objects tabularisation |
| `compress_code.go` | ~200 | String-literal-aware comment + blank-line stripping (safe languages only) |
| `ccr_store.go` / `ccr_http.go` | ~190 | Reversible compress-cache-retrieve store + `/v1/compress/retrieve` |
| `prose_compressor.go` | ~90 | Client for the self-hosted LLMLingua-2 prose endpoint |
| `queueaware.go` | ~140 | vLLM `/metrics` scraper for queue-aware load balancing |
| `kvtree.go` / `kv_index.go` | ~360 | KV cache-aware routing (radix prefix trees) |
| `sessions.go` / `sessions_http.go` | ~330 | Stateful sessions (`kr_sessions`, hydration, TTL sweeper) |
| `qualitygate.go` / `schema_validate.go` | ~360 | Quality gate + JSON-Schema response validation |
| `tenant_ratelimit.go` | ~340 | Per-tenant token-bucket rate limiting |
| `shadow_router.go` / `cost_forecast.go` / `dpo_export.go` | ~620 | Shadow routing, cost forecasting, DPO export |
| `metrics.go` | 184 | Prometheus text format metrics |
| `ratelimit.go` | 137 | Token bucket rate limiter per service |
| `middleware.go` | 125 | Auth, CORS, logging, SSRF validation |
| `audit.go` | 146 | PII-redacted request/response logging |
| `qwen.go` | 78 | Qwen3 thinking mode injection and tag stripping |
| `stats.go` | 74 | Live request statistics |
| `ui.go` | 38 | Embedded web UI (Go embed) |
| `ui/index.html` | ~900 | Dashboard, flow builder, backend manager, cost analysis, config editor |
| `api.go` | 284 | Rules/budgets/config CRUD API endpoints |
| `graphify_middleware.go` | 268 | Pre-stage that compresses or augments messages before classifier |
| `graphify_retrieve.go` | 143 | Hybrid retrieval (pgvector cosine + BM25 reranking) |
| `graphify_embed.go` | 284 | Embedder interface + local-st / Gemini / OpenAI implementations |
| `graphify_chunks.go` | 144 | Content-aware chunker (markdown, code, char-window) |
| `graphify_ingest.go` | 266 | Concurrent walk + embed + upsert pipeline |
| `graphify_schema.go` | 74 | `kr_chunks` table + HNSW + GIN indexes (auto-created) |
| `graphify_watcher.go` | 167 | fsnotify-based live re-ingest (optional) |
| `graphify_cmd.go` | 178 | `ingest`/`graphify stats`/`graphify reset` subcommands |
| `graphify_http.go` | 100 | `POST /v1/retrieve`, `GET /api/graphify` |
| `embedding-service/server.py` | 58 | sentence-transformers Flask sidecar (not in router binary) |
| `agent-gateway/` | ~3,063 | Separate Go module exposing CLI agents as OpenAI endpoints (port 8055) |

## Key Design Decisions

**Single binary with embedded UI.** No Node.js, no separate frontend build. `go:embed` bundles the HTML/JS into the Go binary. One file to deploy.

**YAML config with hot-reload.** Edit the file, rules update in 5 seconds. No restart, no downtime. API can also update rules programmatically.

**Heuristic classifier, not ML.** The auto-tier classifier uses keyword matching and structural analysis, not a neural network. This means zero latency overhead, no additional dependencies, and deterministic behaviour. If classification accuracy matters, callers can set `X-Kronaxis-Tier` explicitly.

**Jaccard similarity for quality validation.** A simple word-overlap metric rather than embedding cosine similarity. This avoids needing to call an embedding model (which would add latency and cost to the validation loop). Accuracy is lower but sufficient for detecting gross quality degradation.

**Business Source License 1.1.** Source-available. Non-commercial internal use is permitted under the Additional Use Grant; commercial production use before the Change Date (9 May 2031) requires a commercial licence from Kronaxis Limited (`contact@kronaxis.co.uk`). After the Change Date the Licensed Work converts automatically to the Apache License, Version 2.0. Your config file (with specific backends, rules, and API keys) is your data and stays private.

**Graphify lives in the router, not as a sidecar.** Token-saving retrieval is conceptually a routing decision -- "send fewer tokens to whichever backend gets picked" stacks naturally with the router's existing levers (cache, classifier, batch). The embedding sidecar is the only out-of-process piece, and only because Python's sentence-transformers ecosystem is the one to use. Retrieval, chunking, watching, and ingestion are all in-process Go for zero per-request hops.

**Agent-gateway is a sibling, not in the router.** Stateless LLM proxying and agentic-loop orchestration have different lifecycles (request/response vs. workspace + subprocess + tool surface). Keeping them separate means the router stays focused on cost-routing while the gateway can iterate on agent semantics. The router treats the gateway as a regular `type: openai` backend; nothing about the routing logic needs to change.

## Performance

Benchmarked against a mock backend (instant responses) to isolate pure router overhead.

| Metric | Value |
|--------|-------|
| **Throughput** | 22,770 req/s at 500 concurrent |
| **P50 latency** | 5.4ms (200 concurrent) |
| **P99 latency** | 42ms (200 concurrent) |
| **Binary size** | 9.9 MB |
| **Memory** | 2.1 MB (constant under load) |

A real LLM call takes 500ms-30s. The router adds 2-5ms. It will never be the bottleneck in any deployment where the backend is an actual LLM.
