# Twitter/X Thread (5 tweets)

## Tweet 1
We cut our LLM inference costs by 94%. Not by switching providers. By stopping sending every request to the expensive model.

80% of LLM tasks are bookkeeping. You do not need a CFO to fill in accounts receivable.

Open-sourced the tool that does this. Thread:

## Tweet 2
The problem: every service in your stack has its own LLM fallback code. Some call GPT-4 for JSON extraction. Others use Claude for entity tagging. Nobody knows which service is burning the budget. Nobody wrote the routing logic because it is boring glue code.

## Tweet 3
Kronaxis Router sits between your apps and your models. Auto-classifies each prompt (extraction? reasoning? summarisation?) and routes to the cheapest model that handles it.

A 9B handles 80% of our traffic. The 27B only sees the 20% that actually needs it. Same quality. 94% cost reduction on the routed traffic.

## Tweet 4
Key features:
- Prompt auto-classification, configurable tiers
- Failover + retry across 7 providers
- 50% off batch API where eligible
- Response caching
- Quality validation (auto-promotes if cheap model degrades)
- Per-service cost budgets
- LoRA adapter routing
- Prometheus + embedded dashboard

## Tweet 5
Single Go binary. 70 tests. Apache 2.0. Python + TypeScript SDKs. Helm chart.

No cloud dependency. Runs in front of local vLLM, Ollama, or any OpenAI-compatible endpoint.

GitHub: https://github.com/kronaxis/kronaxis-router
