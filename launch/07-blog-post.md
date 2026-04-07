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

## Numbers

- Single Go binary, 9.9MB
- 81 tests, 100% classifier accuracy on labelled evaluation set
- 22,770 req/s throughput, 5ms P50 latency, 2MB memory under load
- Apache 2.0

GitHub: https://github.com/Kronaxis/kronaxis-router
