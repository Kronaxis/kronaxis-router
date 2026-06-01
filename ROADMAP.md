# Kronaxis Router Roadmap

Where we're going and why. Features are ordered by implementation priority, not feature number.

## What exists today

Kronaxis Router is a production-grade LLM proxy (~9,000 lines of Go) that routes requests to the cheapest model capable of delivering the required output quality. It already ships with:

- Cost-optimised YAML routing rules with priority ordering and failover
- 8 backend types (vLLM, OpenAI, Anthropic, Gemini, Ollama, Groq, Together, Fireworks)
- LoRA adapter routing (route by adapter name to the vLLM instance that has it loaded)
- Async batch API for 50% off on 7 providers
- Throughput batching (multi-prompt consolidation for vLLM)
- Response caching (SHA-256 keyed, deterministic requests)
- Per-service daily budgets with downgrade/reject actions
- Per-service token bucket rate limiting
- Quality gates (sequential and parallel modes)
- Adaptive prompt classification (complexity scoring with feedback loop)
- A/B testing framework
- Prometheus metrics, PII-redacted audit logging
- Built-in MCP server (12 tools for Claude Code, Cursor, Claude Desktop)
- Embedded web UI (dashboard, flow builder, backend manager, cost analysis)
- Hot-reloadable config, single binary, 2.1 MB memory under load

### Added 2026-05-10: v0.3.0 release (see CHANGELOG)

- **KV cache aware routing (Phase 1)**: radix tree per backend keyed on chunked prefix hashes; biases routing toward whichever backend has a deep prefix match (vLLM KV is presumed warm there). Atomic lastSeen for race free concurrent walks. Endpoint `GET /api/kv-trees`.
- **Stateful sessions (Phase 1)**: `kr_sessions` Postgres table; client uploads full transcript once via `X-Kronaxis-Session-Create: true`, then sends only new turns via `X-Kronaxis-Session-ID: <id>`. TTL sweeper, 30 second hot cache. Endpoints `GET/DELETE /v1/sessions[/<id>]`.
- **Anthropic cache breakpoint injection**: opt in per backend via `cache_breakpoints: true`. Injects `cache_control: {"type": "ephemeral"}` markers on the stable prefix; stacks multiplicatively with sessions.
- **Schema validated quality gates (Phase 2)**: santhosh-tekuri/jsonschema/v5 with FNV keyed compile cache; silent retry on next backend on violation.
- **DPO export to JSONL (Phase 2)**: non blocking buffered writer with key redaction and milestone audit logs. Enabled via `DPO_EXPORT_PATH` env var. Endpoint `GET /api/dpo`.
- **Cost forecast and shadow routing (Phase 3)**: linear extrapolation per service with budget ETA; goroutine fired shadow comparison with Jaccard similarity scoring. Endpoints `GET /api/costs/forecast`, `GET /api/shadow/stats`.
- **Cost Lab UI** at `/cost-lab`: single page dashboard, four tabs (Today / Forecast / Shadow / DPO), auto refresh every 10 s, linked from the main UI nav.
- **Release engineering**: Docker images (static distroless 26 MB + full image), Goreleaser produces deb / rpm / tar.gz / zip across linux/darwin/windows x amd64/arm64, Homebrew tap formula hand committed via `scripts/publish-homebrew-formula.sh`.
- **55+ tests** added covering KV pinning, sessions, schema gates, DPO export, shadow routing, Jaccard similarity, cache breakpoint injection; all green under `go test -race ./...`.

### Added 2026-05-09 (see CHANGELOG)

- **CLI-Agent Gateway expansion (the framework)** -- `agent-gateway/` (port 8055) graduated from a Claude-Code-specific wrapper into a tiered-registry framework that exposes any genuine TUI agent CLI behind a single OpenAI-compatible endpoint. Six built-in profiles: `claude-cli`, `codex-cli`, `aider` (first-class, deep stream parsers) plus `gemini-cli`, `grok-cli`, `llm` (supported, generic streamer). Universal account pool generalised from the previous Anthropic-only pool with provider-aware cooldowns. Profile-declared workspace lifecycle (worktree-ephemeral / dir-ephemeral / stateless). Submodel surface (`model: <agent>/<submodel>` parsed and validated against profile allowlist). Profile-declared graphify default. New API: `GET/POST /v1/agents`, `GET/DELETE /v1/agents/<name>`, `POST /v1/accounts/test`, extended `GET /v1/accounts`. New CLI: `kronaxis-router agents register|list|sync|remove|test`. New router endpoint: `GET /api/agents`. Idempotent rule synthesis writer (`synth.go`) preserves comments + unrelated keys. 50+ tests pass under `go test -race`.
- **BSL 1.1 relicense** -- repo relicensed from Apache 2.0 to Business Source License 1.1, converts to Apache 2.0 on 9 May 2031 (Change Date). Source-available with non-commercial Additional Use Grant; commercial production use before the Change Date requires a separate licence.

### Added 2026-05-08 (see CHANGELOG)

- **graphify pre-stage** -- token-saving RAG that runs before classifier and cost routing. pgvector + sentence-transformers (default `BAAI/bge-small-en-v1.5`, swappable to gemini/openai). Hybrid retrieval (HNSW cosine + BM25 reranking). Modes: `compress`, `augment`, `auto`, `off`. Live re-ingest via fsnotify watcher. Adds `POST /v1/retrieve`, `GET /api/graphify`, `kronaxis-router ingest <paths>`, and graphify Prometheus counters. Disabled by default.
- **agent-gateway sub-service** at `agent-gateway/` (port 8055) -- initial OpenAI-compatible HTTP wrapper around CLI agents. Superseded by the 2026-05-09 framework expansion above.
- **Claude Code OAuth subscription pooling (personal use only)** -- in-process gate at first run requires user to confirm non-commercial intent; if they pick "commercial" the subscription path is disabled and only API-key adapters remain available.

## Phase 1: Local cluster intelligence

These features make Kronaxis the best router for anyone running multiple vLLM instances. They compound on each other and require no new dependencies.

### KV Cache-Aware Routing (Radix Tree Pinning) — shipped in v0.3.0

**Problem:** Round-robin routing across a vLLM cluster forces each node to recompute the KV cache for multi-turn conversations. A 100k-token system prompt gets reprocessed on every turn if the request lands on a different node.

**Solution:** Kronaxis maintains a lightweight radix tree keyed on prompt prefix hashes. When a follow-up message arrives, it routes to the node that already has the KV cache warm from the previous turn. The KV cache hit rate improvement on local clusters is massive: TTFT drops from seconds to milliseconds for cached prefixes.

```yaml
backends:
  - name: vllm-node-1
    url: "http://gpu-1:8000"
    type: vllm
    kv_pinning: true      # Enable prefix tracking for this backend
```

### Queue-Aware Load Balancing — shipped

**Problem:** Static priority routing can overwhelm a single node while others sit idle. Health checks tell you if a node is alive, not if it's busy.

**Solution:** Kronaxis periodically scrapes the `/metrics` endpoint of local backends, reading `vllm:num_requests_waiting` and `vllm:num_requests_running`. It factors queue depth into backend selection, routing to the node with the lowest active queue. Combined with KV pinning, this means: route to the node with the warmest cache, unless it's overloaded.

```yaml
server:
  queue_aware_routing: true           # Enable queue-depth scraping
  queue_scrape_interval: 5s           # How often to read /metrics
```

### Stateful Session Management — shipped in v0.3.0

**Problem:** Agentic workflows (Claude Code, Cursor, custom agents) re-upload the entire conversation context with every HTTP request. A 100k-token system prompt gets sent 50 times during a coding session. This wastes bandwidth, increases latency, and costs money on metered APIs.

**Solution:** The client sends the full context once. Kronaxis stores it and returns a `kronaxis-session-id`. On subsequent turns, the client sends only the session ID and the new message. Kronaxis hydrates the full prompt array server-side before forwarding.

```bash
# First request: full context
curl http://localhost:8050/v1/chat/completions \
  -H "X-Kronaxis-Session-Create: true" \
  -d '{"messages": [{"role": "system", "content": "...100k tokens..."}]}'
# Response header: X-Kronaxis-Session-ID: sess_abc123

# Subsequent requests: session ID only
curl http://localhost:8050/v1/chat/completions \
  -H "X-Kronaxis-Session-ID: sess_abc123" \
  -d '{"messages": [{"role": "user", "content": "Just the new question"}]}'
```

This also unlocks provider-side cache optimisation: because Kronaxis controls the message array, it can separate static context from dynamic messages and inject provider-specific cache breakpoints (Anthropic's ephemeral markers, OpenAI's cache hints) at optimal boundaries.

## Phase 2: Production safety

### Schema-Validated Quality Gates — shipped in v0.3.0

**Problem:** The "cheap model first" strategy saves money but breaks production when the cheap model hallucinates invalid JSON. The existing quality gate validates by comparing token overlap against a reference model. That catches gross quality degradation but not structural failures.

**Solution:** Users supply a JSON Schema in the request. Kronaxis validates the cheap model's output against it. If validation fails, it silently retries on the fallback (expensive) model. The client always receives schema-valid JSON.

```bash
curl http://localhost:8050/v1/chat/completions \
  -H "X-Kronaxis-Response-Schema: {\"type\":\"object\",\"required\":[\"name\",\"score\"]}" \
  -d '{"model": "default", "messages": [...]}'
```

This makes tier-2 routing safe for production extraction pipelines. 90% of requests succeed on the cheap model; the 10% that fail get transparently escalated.

### Circuit Breaking

**Problem:** The current health check interval is 30 seconds. If a cloud provider throws five 503s in 2 seconds, the router keeps sending traffic to it until the next probe.

**Solution:** Track error timestamps per backend. If N errors occur within M seconds, open the circuit immediately and failover. Auto-recover on the next successful health check.

```yaml
backends:
  - name: cloud-fast
    circuit_breaker:
      error_threshold: 5          # Errors to trip
      window_seconds: 10          # Within this window
      recovery_probe_interval: 15s # How often to test recovery
```

### DPO Dataset Export (Self-Teaching Flywheel) — shipped in v0.3.0

**Problem:** Fine-tuning data for local models is expensive to create manually.

**Solution:** Every time a quality gate fallback fires (cheap model fails, expensive model succeeds), Kronaxis logs the pair: cheap output as "rejected", expensive output as "chosen". This automatically builds a Direct Preference Optimization dataset. Over time, fine-tune your local models on this data and they fail less, reducing cloud spend further.

```yaml
quality_gate:
  dpo_export:
    enabled: true
    output_path: "dpo_training_data.jsonl"
    min_pairs: 100                # Minimum pairs before export
```

The router's job is to emit the dataset. Training is handled by your existing fine-tuning infrastructure.

## Phase 3: Operational intelligence

### Shadow Routing (Migration Testing) — shipped in v0.3.0

**Problem:** Switching from GPT-4o to Gemini Flash could save 80%, but how do you prove quality is comparable before committing?

**Solution:** Shadow mode sends 100% of traffic to the primary backend, silently duplicates a configurable percentage to a shadow backend, and compares outputs. The dashboard shows: "If you switch to Gemini Flash, you save $X/day with 97% output similarity."

```yaml
ab_tests:
  - name: gemini-migration-test
    match: { service: "my-api" }
    variant_a: cloud-expensive       # Primary (returned to caller)
    variant_b: cloud-cheap           # Shadow (logged, not returned)
    split_pct: 10                    # Shadow 10% of traffic
    mode: shadow                     # Don't return shadow responses
```

### Cost Forecasting — shipped in v0.3.0

**Problem:** Budget enforcement catches you at the limit. By then it's too late to adjust.

**Solution:** Linear extrapolation from morning spend: "At current burn rate, the `my-api` service will hit its $50 daily budget at 2:14 PM." Exposed via `/api/costs/forecast` and the MCP server.

### Predictive SLA Routing

**Problem:** Static fallback rules still route to APIs experiencing stealth latency spikes.

**Solution:** Track a rolling P50/P95 TTFT and tokens-per-second for every backend. If a backend's P95 exceeds the SLA target, proactively reroute before the next request fails. Start reactive (route away from spikes), evolve to predictive.

```yaml
rules:
  - name: latency-sensitive
    match: { priority_level: interactive }
    backends: [local-fast, cloud-fast, cloud-powerful]
    max_ttft_ms: 800               # SLA constraint
```

### MCP Server Expansion

Expose session management, cost forecasting, and shadow routing via new MCP tools. Claude Code users can say "create a session for this context" or "show me today's cost forecast" conversationally.

## Phase 4: Token optimisation

### In-Flight Context Compaction

Intercept and compact payloads before forwarding using RTK-style filters: log deduplication, JSON array truncation, whitespace minification. Opt-in per rule, gated by the classifier to avoid destroying semantic meaning in prompts that are asking the model to analyse specific content.

### Provider Cache Optimisation

Auto-inject provider-specific cache breakpoints (Anthropic ephemeral markers, OpenAI cache hints) by detecting which parts of the message array are stable across turns. Natural extension of session management.

## Future directions (design phase)

These ideas are architecturally sound but require more design work before implementation. They will not be built until Phases 1-3 are complete and shipping.

- **DLP Redaction** -- Request-time PII swap with response-time rehydration. Session affinity critical.
- **Intent-Based Routing** -- ONNX classifier for automatic CODE/MATH/CREATIVE dispatch. Must benchmark against keyword heuristic.
- **Map-Reduce Fan-Out** -- Split large documents across a fleet, synthesise with a strong model.
- **Request Priority Queuing** -- True priority queue where interactive pre-empts background batch.
- **Model Version Pinning** -- Detect silent cloud model version changes via response headers.
- **Multi-Region Awareness** -- Geographic routing based on RTT to backends.
- **Speculative Decoding** -- Draft on local, verify on cloud. GPT-4 quality at 8B speed.
- **Adversarial Consensus** -- Route to 3 models, arbiter resolves disagreements.
- **Spot-Market Arbitrage** -- Live price tracking across providers, route to cheapest that meets SLA.

## Non-goals

These ideas are technically fascinating but do not belong in a routing proxy, at least not in the near term:

- **eBPF kernel bypass** -- The router handles 22,770 req/s at 20ms latency. It is not the bottleneck. GPU memory bandwidth constrains LLM throughput, not TCP stack overhead.
- **Runtime LoRA synthesis** -- SVD merge of adapters is mathematically simple but vLLM does not support hot-loading merged adapters without reinitialisation. This belongs in the inference server.
- **Branch prediction** -- Predicting prompts from typing events requires deep UI integration and >80% accuracy to justify wasted compute. Demo, not product.
- **Shared swarm memory** -- A distributed context bus for multi-agent systems is a distributed systems problem, not a routing problem. Closer to building semantic Redis.

## Contributing

The best way to contribute is to pick a Phase 1 or Phase 2 feature and open a PR. Each feature is designed to be self-contained (one new Go file + integration into the routing path) with clear test requirements.

See [IMPLEMENTATION_PLAN.md](docs/IMPLEMENTATION_PLAN.md) for file-level specs, config schema changes, and test requirements for each feature.

## Licence

Business Source License 1.1. Source-available with a non-commercial Additional Use Grant; converts to Apache License, Version 2.0 on 9 May 2031 (the Change Date). Commercial production use before that date requires a commercial licence -- contact `contact@kronaxis.co.uk`. See [LICENSE](LICENSE) for the full terms.
