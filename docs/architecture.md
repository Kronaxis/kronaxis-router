# Architecture

## Overview

```
                    ┌──────────────────────────────────────────┐
                    │           Kronaxis Router                │
                    │              :8050                       │
   Requests ──────>│                                          │
                    │  ┌─────────┐  ┌────────┐  ┌──────────┐ │
                    │  │Classifier│  │ Router │  │ Batcher  │ │──> Local vLLM
                    │  │(auto-tier)│  │(rules) │  │(throughput)│ │──> Vast.ai
                    │  └─────────┘  └────────┘  └──────────┘ │──> Gemini
                    │  ┌─────────┐  ┌────────┐  ┌──────────┐ │──> OpenAI
                    │  │  Cache  │  │ Budget │  │Rate Limit│ │──> Ollama
                    │  └─────────┘  └────────┘  └──────────┘ │
                    │  ┌─────────┐  ┌────────┐  ┌──────────┐ │
                    │  │ Quality │  │ A/B    │  │  Batch   │ │
                    │  │Validator│  │ Tests  │  │  Manager │ │
                    │  └─────────┘  └────────┘  └──────────┘ │
                    │  ┌─────────┐  ┌────────┐  ┌──────────┐ │
                    │  │ Metrics │  │ Audit  │  │  Web UI  │ │
                    │  └─────────┘  └────────┘  └──────────┘ │
                    └──────────────────────────────────────────┘
```

## Request Flow

1. **Receive** -- OpenAI-compatible POST to `/v1/chat/completions`
2. **Extract** -- Parse request body, extract `X-Kronaxis-*` headers
3. **Classify** -- Auto-assign tier if not explicitly set (classifier.go)
4. **Cache check** -- Return cached response for deterministic requests (cache.go)
5. **Budget check** -- Reject or prepare downgrade if budget exceeded (costs.go)
6. **Auto-batch** -- For `bulk` priority on batch-capable backends, submit async (batch.go)
7. **Route** -- Evaluate rules in priority order, get candidate backends (router.go)
8. **Budget downgrade** -- Prepend cheaper backend if over budget
9. **A/B test** -- Override backend if A/B test applies (abtest.go)
10. **Adapt** -- Inject Qwen thinking mode disable, set model name (qwen.go)
11. **Dispatch** -- Forward to first healthy candidate (proxy.go)
12. **Failover** -- On 5xx/error, try next candidate. Retry once with 500ms backoff.
13. **Post-process** -- Strip think tags, inject branding, compress response
14. **Cache store** -- Cache deterministic successful responses
15. **Quality sample** -- Randomly validate cheap-model output against reference
16. **Log** -- Record to stats, Prometheus metrics, cost tracker, audit log
17. **Return** -- Send response to caller with branding headers

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
| `compress.go` | 140 | Prompt compression (whitespace, dedup, truncation) |
| `metrics.go` | 184 | Prometheus text format metrics |
| `ratelimit.go` | 137 | Token bucket rate limiter per service |
| `middleware.go` | 125 | Auth, CORS, logging, SSRF validation |
| `audit.go` | 146 | PII-redacted request/response logging |
| `qwen.go` | 78 | Qwen3 thinking mode injection and tag stripping |
| `stats.go` | 74 | Live request statistics |
| `ui.go` | 38 | Embedded web UI (Go embed) |
| `ui/index.html` | ~900 | Dashboard, flow builder, backend manager, cost analysis, config editor |
| `api.go` | 284 | Rules/budgets/config CRUD API endpoints |

## Key Design Decisions

**Single binary with embedded UI.** No Node.js, no separate frontend build. `go:embed` bundles the HTML/JS into the Go binary. One file to deploy.

**YAML config with hot-reload.** Edit the file, rules update in 5 seconds. No restart, no downtime. API can also update rules programmatically.

**Heuristic classifier, not ML.** The auto-tier classifier uses keyword matching and structural analysis, not a neural network. This means zero latency overhead, no additional dependencies, and deterministic behaviour. If classification accuracy matters, callers can set `X-Kronaxis-Tier` explicitly.

**Jaccard similarity for quality validation.** A simple word-overlap metric rather than embedding cosine similarity. This avoids needing to call an embedding model (which would add latency and cost to the validation loop). Accuracy is lower but sufficient for detecting gross quality degradation.

**Apache 2.0 licence.** No restrictions on commercial use, modification, or redistribution. The config file (with your specific backends, rules, and API keys) stays private.

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
