# How We Cut LLM Inference Costs by 94%

## The invoice that started it

In February, our monthly LLM inference bill was dominated by a single pattern: every service in the stack sent every request to the same model. JSON extraction, entity tagging, document summarisation, multi-step reasoning: all hitting a 27B parameter model (or worse, a cloud frontier API at $10/million tokens).

The 27B produced correct output for all of these tasks. But most of them did not need it.

## The bookkeeper principle

A CFO can fill in accounts receivable. But a bookkeeper is 50x cheaper and does the job just as well. You would not pay CFO rates for bookkeeping.

LLM workloads follow the same distribution. In our production traffic, roughly 80% of requests were structured tasks: extract these fields from this document, summarise this text in three bullet points, classify this support ticket. A 9B model handles all of these with equivalent output quality.

The remaining 20% genuinely needed the larger model: multi-step reasoning, code generation with complex constraints, synthesis across long context windows.

## The cost arithmetic

| Model | Cost per 1M tokens | Typical tasks |
|---|---|---|
| Local 9B (vLLM, quantised) | $0.005 | Extraction, classification, summarisation, translation |
| Local 27B (vLLM, quantised) | $0.02 | Reasoning, code generation, creative writing |
| Gemini Flash (cloud) | $0.60 | Overflow, burst capacity |
| Gemini Pro / GPT-4 (cloud) | $10.00 | Reference validation only |

If 80% of your traffic moves from the $0.60 tier to the $0.005 tier, that is a 94% cost reduction on those requests. Even moving from $0.02 to $0.005 on the easy tasks frees the 27B's GPU capacity for the requests that actually benefit from it.

## How the routing works

Kronaxis Router is a single Go binary that sits between your applications and your model backends. Every incoming request passes through a lightweight classifier (rule-based, no LLM call, under 1ms overhead) that assigns a task category:

- **Structured extraction:** JSON schema present, output format constrained, short expected output
- **Summarisation:** short expected output relative to input, condensation signals
- **Classification/tagging:** enumerated output set, single-label patterns
- **Reasoning:** "analyse", "compare", "evaluate", multi-step instructions, long expected output
- **Code generation:** code block formatting, language specifications

Each category maps to a model tier in config. The classifier is deliberately conservative: ambiguous cases route to the higher tier.

## The quality validation loop

Trusting a cheap model blindly is a bad idea. Model performance varies across tasks, and what works today might degrade after a provider update or a data distribution shift.

Kronaxis Router samples a configurable percentage of routed requests (default 5%) and sends the same prompt to both the assigned model and a reference model. Results feed into a sliding window per task category. If the cheap model's quality drops below the configured threshold, that category auto-promotes to the next tier.

This closes the feedback loop. You get cost savings by default with an automatic safety net.

## Response caching

Deterministic requests (same prompt, same parameters, temperature 0) get the same output. The router caches responses keyed on a hash of the request body. For extraction and classification tasks where the output should be identical for identical input, this eliminates redundant inference calls entirely.

In our workload, the cache hit rate on extraction tasks is around 30%, which is another meaningful cost reduction on top of the routing savings.

## What we did not build

Kronaxis Router does not normalise provider APIs across 100 different backends. LiteLLM does that well. The router only speaks OpenAI-compatible API (which covers most backends including vLLM, Ollama, and the major cloud providers).

It also does not do prompt engineering, output parsing, or chain orchestration. It solves one problem: which model should handle this request, and what happens when that model is unavailable or underperforming.

## Getting started

```bash
git clone https://github.com/kronaxis/kronaxis-router.git
cd kronaxis-router
go build -o kronaxis-router .
./kronaxis-router
```

Open http://localhost:8050 for the web dashboard. Point your services at `http://localhost:8050/v1/chat/completions`.

Single binary. 70 tests. Apache 2.0. Python and TypeScript SDKs. Helm chart for Kubernetes.

GitHub: https://github.com/kronaxis/kronaxis-router
