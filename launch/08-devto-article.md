---
title: "Stop Paying Frontier Prices for Tasks a Local Model Handles Fine"
published: true
description: "Small LLMs got good. Here's an open-source Go proxy that auto-classifies prompts and routes to the cheapest capable model, with a quality validation safety net."
tags: go, llm, opensource, devops
canonical_url: https://kronaxis.co.uk/blog/llm-routing-cost-savings
cover_image:
---

Small open-weight models got good. Qwen 9B, Llama 8B, Gemma 4B handle 80% of production LLM workloads (extraction, classification, summarisation, tagging) with output quality indistinguishable from frontier APIs.

The remaining 20% genuinely needs the big model. But nobody routes. Every request hits the same endpoint. You are paying $3-15 per million tokens for work that a free local model does identically.

## The cost arithmetic

| Backend | Cost per 1M tokens | Typical tasks |
|---|---|---|
| Local 9B (Ollama/vLLM) | ~$0.005 | Extraction, classification, summarisation |
| Local 27B (vLLM, quantised) | ~$0.02 | Reasoning, code generation |
| Cloud API (Gemini Flash) | $0.15-0.60 | Overflow |
| Frontier API (Claude, GPT-4) | $3-15.00 | Complex reasoning |

Route 80% of traffic from the frontier tier to a local 9B and your blended cost drops from ~$10 to ~$0.50 per million tokens.

## How Kronaxis Router works

Single Go binary. Sits between your app and your model backends. Every request passes through a lightweight rule-based classifier (no LLM call, under 1ms) that assigns a task category:

- **Structured extraction:** JSON schema, constrained output -> cheap model
- **Classification:** single-label, yes/no, sentiment -> cheap model
- **Summarisation:** condensation, bullet points -> cheap model
- **Reasoning:** "analyse", "compare", multi-step -> capable model
- **Code generation:** language specs, complex constraints -> capable model

The classifier is deliberately conservative. Ambiguous cases route to the more capable model. Evaluated against 25 labelled prompts: 100% accuracy.

## The quality safety net

Routing to a cheap model blindly is a bad idea. The router samples 5% of cheap-model responses and validates them against a reference model. Sliding window per task category. If quality drops below threshold, that category auto-promotes to the next tier.

Savings by default. Automatic safety net.

## Architecture

```
Client App  -->  Kronaxis Router  -->  Backend A (local 9B, Ollama/vLLM)
                      |           -->  Backend B (local 27B, vLLM)
                      |           -->  Backend C (Gemini Flash)
                      |
                  Classifier (rule-based, <1ms)
                  Cache Layer (SHA-256, temp=0 only)
                  Budget Enforcer (downgrade on limit)
                  Quality Validator (5% sampling)
                  Batch Router (50% off on 7 providers)
                  Metrics Collector (Prometheus)
```

### Why Go

Single static binary. No Python runtime, no Node, no containers required. 2MB memory under full load. 22,770 req/s throughput. The router will never be the bottleneck when LLM inference takes 500ms-30s.

### Backend failover

3 consecutive failures marks a backend DOWN. 1 success recovers it. When a request fails, the router tries the next backend in the chain. Local vLLM crash gracefully overflows to cloud without client-side changes.

### LoRA adapter routing

If your vLLM instance serves multiple LoRA adapters, the router rewrites the `model` field to the correct adapter based on request metadata. The client sends a standard OpenAI-compatible request and never needs to know which adapter exists.

### Batch API routing

Seven providers offer 50% off on batch API requests. The router handles this transparently: tag a request as `bulk` priority and it auto-submits to the provider's batch endpoint. For overnight jobs, this halves your cloud costs on top of the routing savings.

### Response caching

Deterministic requests (same prompt, temperature 0) served from an in-memory SHA-256 keyed cache. 30% hit rate on extraction workloads in our production traffic.

### Budget enforcement

Set a daily dollar limit per service. When hit, the router downgrades to a cheaper model instead of returning errors. Your pipeline keeps running.

## How this compares to alternatives

| Feature | Kronaxis Router | LiteLLM | OpenRouter | Portkey | Martian |
|---|---|---|---|---|---|
| Self-hosted | Yes | Yes | No | No | No |
| Cost-based routing | Automatic | Manual | Some | Manual | ML-based |
| Quality validation | Closed loop | No | No | No | Implicit |
| Batch API (50% off) | 7 providers | No | No | No | No |
| Response caching | Built in | No | No | No | No |
| Budget enforcement | Downgrade | Alerts | No | Alerts | No |
| LoRA routing | Yes | No | No | No | No |
| Memory | 2MB | 300MB+ | SaaS | SaaS | SaaS |
| Throughput | 22K req/s | ~2K req/s | N/A | N/A | N/A |
| Provider count | 4 types | 100+ | 200+ | 15+ | 100+ |
| Price | Free | Free/$150+ | Margin | $99+/mo | Usage |
| Licence | BSL 1.1 | MIT | Closed | Closed | Closed |

LiteLLM is a universal gateway. OpenRouter is zero-setup SaaS. Portkey is observability. Martian is ML routing. Kronaxis Router is a cost optimiser. Different tools for different problems.

## Getting started

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | bash

# Auto-detect local models and API keys, generate config
kronaxis-router init

# Start
kronaxis-router
```

Also available: `brew install kronaxis/tap/kronaxis-router`, `go install`, Docker, deb/rpm.

For Claude Code and Cursor: `kronaxis-router init --claude` or `kronaxis-router init --cursor` configures the built-in MCP server for conversational management of backends, costs, and rules.

81 tests. BSL 1.1.

**GitHub:** [github.com/Kronaxis/kronaxis-router](https://github.com/Kronaxis/kronaxis-router)

**Full blog post:** [kronaxis.co.uk/blog/llm-routing-cost-savings](https://kronaxis.co.uk/blog/llm-routing-cost-savings)
