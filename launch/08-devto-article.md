# Building a Cost-Optimised LLM Proxy in Go

*Technical walkthrough of Kronaxis Router's architecture, routing algorithm, and design decisions.*

## Why Go, and why a proxy

Every team running LLM workloads in production eventually writes some version of the same code: try the cheap model, check if the output is good enough, fall back to the expensive model if not, handle timeouts, log costs. This logic gets duplicated across services, implemented inconsistently, and maintained by whoever last touched it.

We extracted this into a standalone proxy. Go was the obvious choice: single static binary, no runtime dependencies, excellent concurrency primitives for handling thousands of concurrent streaming requests, and low memory overhead for a process that mostly proxies bytes.

## Architecture overview

```
Client App  -->  Kronaxis Router  -->  Backend A (local 9B vLLM)
                      |           -->  Backend B (local 27B vLLM)
                      |           -->  Backend C (Gemini Flash)
                      |
                  Classifier
                  Cache Layer
                  Budget Enforcer
                  Quality Validator
                  Metrics Collector
```

The router accepts any OpenAI-compatible chat completion request. The request flows through:

1. **Cache check:** Hash of request body checked against response cache.
2. **Classification:** Rule-based task classifier assigns a category and tier.
3. **Budget check:** Reject or downgrade if service budget exceeded.
4. **Backend selection:** Within the tier, select healthy backends in preference order.
5. **Failover:** Try each candidate. On 5xx, move to the next. One retry with 500ms backoff.
6. **Quality sampling:** Fire an async goroutine to validate against reference model.

## The classifier

The classifier is deliberately not an LLM. Adding an LLM call to classify every request would defeat the purpose. Instead, it uses weighted heuristics applied to the request:

- JSON schema or structured output format in system prompt -> Tier 2
- Short expected output (max_tokens < 200) -> Tier 2
- Keywords: "extract", "classify", "score" -> Tier 2
- Keywords: "plan", "analyse", "design", "write a" -> Tier 1
- Temperature 0 -> Tier 2 (deterministic = extraction)
- Temperature >= 0.8 -> Tier 1 (creative = reasoning)
- Long context (> 2000 tokens) -> Tier 1

The classifier runs in under 1ms. Ambiguous cases default to the higher tier.

## Backend health and failover

Each backend tracks health via periodic probes and actual request outcomes:

- 3 consecutive failures -> backend marked DOWN
- 1 success after failures -> backend recovers to HEALTHY
- Cloud backends (Gemini, OpenAI) tracked by request success only (no probes)

When a request fails, the router tries the next candidate in the rule's backend list. This means a local vLLM crash gracefully overflows to cloud backends without client-side changes.

## LoRA adapter routing

If your vLLM instance serves multiple LoRA adapters, list them in config. The router rewrites the `model` field to the correct adapter name based on the request's model field or task metadata. The client never needs to know which adapter exists.

## Performance

- Binary size: ~15MB (statically linked)
- Memory: ~30MB baseline
- Latency overhead: median <1ms, P99 <5ms
- Throughput: limited by backend response time, not the router

## How it compares to LiteLLM

LiteLLM normalises 100+ provider APIs. Kronaxis Router decides which model to call. They solve different problems and work well together: Router in front of LiteLLM gives you intelligent routing plus broad provider support.

| Aspect | Kronaxis Router | LiteLLM |
|---|---|---|
| Primary function | Routing decision | API normalisation |
| Auto-classification | Built-in heuristic | Not available |
| Quality validation | Built-in feedback loop | Not available |
| LoRA adapter routing | Built-in | Not available |
| Language | Go (single binary) | Python |

## Getting started

```bash
go build -o kronaxis-router .
./kronaxis-router
# Open http://localhost:8050
```

Apache 2.0. 70 tests. Python and TypeScript SDKs.

GitHub: https://github.com/kronaxis/kronaxis-router
