# Stop Paying Frontier Prices for Tasks a Local Model Handles Fine

## Small models got good. Your routing did not.

Twelve months ago, routing every LLM request to GPT-4 or Claude made sense. The gap between frontier models and open-weight alternatives was real and measurable.

That gap has largely closed for the majority of production workloads. Qwen 9B extracts JSON as accurately as GPT-4o. Llama 8B classifies support tickets with the same precision as Claude. Gemma 4B summarises documents with no measurable quality loss.

The 20% of requests that genuinely need frontier capability (multi-step reasoning, long-context synthesis, complex code generation) still justify the cost. The other 80% do not.

## The cost arithmetic

| Backend | Cost per 1M tokens | Typical tasks |
|---|---|---|
| Local 9B (Ollama/vLLM, consumer GPU) | ~$0.005 | Extraction, classification, summarisation, translation, tagging |
| Local 27B (vLLM, quantised) | ~$0.02 | Reasoning, code generation, creative writing |
| Cloud API (Gemini Flash, GPT-4o-mini) | $0.15-0.60 | Overflow, burst capacity |
| Frontier API (Claude, GPT-4, Gemini Pro) | $3-15.00 | Complex reasoning, reference validation |

If 80% of your traffic moves from the $3-15 tier to the $0.005 tier, that is not an incremental saving. It is a structural cost reduction.

## What Kronaxis Router does

It sits between your application and your models. One URL. Every incoming request passes through a lightweight classifier (rule-based, no LLM call, under 1ms overhead) that determines what the request actually needs:

- **Structured extraction:** JSON schema, constrained output, enumerated fields -> cheap model
- **Classification:** single-label, yes/no, sentiment -> cheap model
- **Summarisation:** condensation, bullet points, one-sentence -> cheap model
- **Reasoning:** "analyse", "compare", multi-step, long output -> capable model
- **Code generation:** language specs, test requirements, complex constraints -> capable model

The classifier is deliberately conservative. Ambiguous cases route to the more capable (expensive) model. The cost of a false negative (missed saving) is dollars. The cost of a false positive (bad output) is trust.

## The quality safety net

Routing to a cheap model blindly is a bad idea. Model performance varies, and what works today might degrade after a provider update.

The router samples 5% of cheap-model responses and validates them against a reference model. Results feed into a sliding window per task category. If quality drops below the configured threshold, that category auto-promotes to the next tier.

This closes the feedback loop. Savings by default, automatic safety net.

## Batch API routing: another 50% off

Seven providers (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks) offer 50% discounts on batch API requests. The catch is they require a different submission flow (file upload, polling, webhook).

Kronaxis Router handles this transparently. Tag a request as `bulk` priority and it auto-submits to the provider's batch endpoint. You get the result via polling or webhook callback. For overnight enrichment jobs, training data generation, or any latency-insensitive workload, this halves your cloud costs on top of the routing savings.

## Response caching

Deterministic requests (same prompt, temperature 0) produce the same output. The router caches responses keyed on a SHA-256 hash of the request body. For extraction and classification pipelines where the same documents get processed repeatedly, this eliminates redundant inference entirely.

In typical workloads, the cache hit rate on extraction tasks runs around 30%.

## Budget enforcement

Set a daily dollar limit per service. When the limit is hit, the router does not fail. It downgrades to a cheaper model. Your pipeline keeps running, just on a smaller model, instead of returning 429 errors at 3pm.

## Getting started

```bash
# Install (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | bash

# Auto-detect your backends and generate config
kronaxis-router init

# Start
kronaxis-router
```

The `init` command probes for local Ollama and vLLM instances and checks your environment for cloud API keys (Gemini, OpenAI, Anthropic, Groq, Together, Fireworks). It generates a config with backends, routing rules, budgets, and rate limits.

Also available via Homebrew (`brew install kronaxis/tap/kronaxis-router`), Go install, or Docker.

For Claude Code and Cursor users: `kronaxis-router init --claude` or `kronaxis-router init --cursor` configures the MCP server automatically, giving your AI assistant tools to manage routing, costs, and backends conversationally.

## What it is not

It is not a universal LLM gateway that normalises 100 provider APIs. LiteLLM does that. It speaks OpenAI-compatible API (which covers vLLM, Ollama, and every major cloud provider).

It does not do prompt engineering, output parsing, or chain orchestration. It solves one problem: which model should handle this request, and what happens when that model is unavailable or underperforming.

## How this compares to alternatives

We built this because nothing else solved the actual problem. Here is an honest comparison.

### vs LiteLLM

LiteLLM is the most established open-source LLM gateway. It supports 100+ providers, has virtual API keys, team-based spend tracking, and a web UI for management. If you need to unify a dozen different provider APIs behind one endpoint, LiteLLM does that well.

Where it does not compete:

| Capability | LiteLLM | Kronaxis Router |
|---|---|---|
| **Intelligent routing** | Manual: you pick the model per request | Automatic: classifier assigns tier, routes to cheapest capable backend |
| **Quality validation** | None | 5% sampling against reference model, auto-promote on degradation |
| **Batch API aggregation** | None | Transparent 50% off across 7 providers |
| **Response caching** | None | SHA-256 keyed, deterministic requests |
| **LoRA adapter routing** | None | Routes to the vLLM instance with the right adapter loaded |
| **Budget enforcement** | Spend tracking (alerts) | Active enforcement: downgrade to cheaper model on limit hit |
| **Runtime** | Python (300MB+ memory, ~2K req/s) | Go (2MB memory, 22K req/s) |
| **Deployment** | pip install + Python runtime | Single binary, zero dependencies |
| **Provider count** | 100+ | 4 types (vLLM, Ollama, OpenAI-compat, Gemini), which covers most backends |

LiteLLM is a universal gateway. Kronaxis Router is a cost optimiser. Different tools for different problems. If you need broad provider coverage and already run Python infrastructure, LiteLLM is a good choice. If you want to minimise LLM spend with automatic quality assurance, this is purpose-built for that.

### vs OpenRouter

OpenRouter is a SaaS proxy with a large model catalogue and zero setup. You get one API with access to hundreds of models from dozens of providers.

The trade-off is cost. OpenRouter adds a margin on top of provider pricing (typically 5-20%). If you are routing to reduce costs, adding a middleman margin goes in the wrong direction. Your data also passes through their infrastructure, which may not work for regulated or sensitive workloads.

OpenRouter does not support local models, has no quality validation, no budget enforcement, and no batch API routing.

| | OpenRouter | Kronaxis Router |
|---|---|---|
| **Setup** | Zero (SaaS) | One binary + config |
| **Cost** | Provider price + margin | Provider price only (self-hosted) |
| **Local models** | No | Yes (Ollama, vLLM) |
| **Data residency** | Their servers | Your infrastructure |
| **Intelligent routing** | Some (fallback) | Full cost-based classification |
| **Quality validation** | No | Yes |
| **Batch API** | No | Yes (50% off) |

### vs Portkey

Portkey is strong on observability: request logging, prompt management, guardrails, A/B testing. If you need a full LLM ops platform, Portkey covers more surface area.

It is a SaaS product with paid plans starting at $99/month. It does not do cost-based routing (you pick the model), does not support local models, and does not aggregate batch APIs. For teams whose primary problem is visibility and governance rather than cost, Portkey is a reasonable choice.

### vs Martian / Not Diamond

Both use ML-trained classifiers to pick the best model per request. This is more sophisticated than rule-based classification: they learn from outcomes rather than relying on keyword heuristics.

The trade-offs: both are SaaS (closed source, usage-based pricing, data leaves your infrastructure), the ML classification adds network latency per request, and neither supports local models or batch API routing.

Our classifier is deliberately simple (rule-based, under 1ms, zero network calls) with a quality validation feedback loop that catches regressions. Less sophisticated, but cheaper, faster, and self-hosted.

### Summary table

| Feature | Kronaxis Router | LiteLLM | OpenRouter | Portkey | Martian |
|---|---|---|---|---|---|
| Self-hosted | Yes | Yes | No | No | No |
| Open source | Apache 2.0 | MIT | No | No | No |
| Cost-based routing | Automatic | Manual | Some | Manual | ML-based |
| Quality validation | Yes (closed loop) | No | No | No | Implicit |
| Batch API (50% off) | 7 providers | No | No | No | No |
| Response caching | Yes | No | No | No | No |
| Budget enforcement | Downgrade on limit | Alerts only | No | Alerts | No |
| LoRA routing | Yes | No | No | No | No |
| Local model support | Ollama + vLLM | vLLM | No | No | No |
| Memory usage | 2MB | 300MB+ | N/A (SaaS) | N/A | N/A |
| Throughput | 22K req/s | ~2K req/s | N/A | N/A | N/A |
| Provider count | 4 types | 100+ | 200+ | 15+ | 100+ |
| MCP integration | Built in | No | No | No | No |
| Price | Free | Free / $150+/mo hosted | Margin per request | $99+/mo | Usage-based |

We win on cost savings, performance, and self-hosted simplicity. LiteLLM wins on provider breadth. OpenRouter wins on zero setup. Portkey wins on observability. Martian wins on routing sophistication.

For teams whose primary goal is spending less on LLM inference without sacrificing output quality, this is the tool.

## Numbers

- Single Go binary, 9.9MB
- 81 tests, 100% classifier accuracy on labelled evaluation set
- 22,770 req/s throughput, 5ms P50 latency, 2MB memory under load
- Apache 2.0

GitHub: https://github.com/Kronaxis/kronaxis-router
