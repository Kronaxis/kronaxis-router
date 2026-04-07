# Hacker News Show HN Post

**Title:** Show HN: Kronaxis Router -- stop paying frontier prices for tasks a $0 local model handles fine

**Body:**

Here is what changed in the last 12 months: small open-weight models got good. Really good. Qwen 9B, Llama 8B, Gemma 4B -- these handle 80% of production LLM workloads (extraction, classification, summarisation, tagging) with output quality indistinguishable from a frontier API. The remaining 20% genuinely needs the big model.

The problem is nobody routes. Every request hits the same endpoint. You are paying $3-15 per million tokens for work that a free local model does identically.

Kronaxis Router fixes this. Single Go binary, sits between your app and your models, classifies each request, sends it to the cheapest backend that can handle it.

The economics:

- Local 9B on a consumer GPU: ~$0.005/1M tokens (electricity only)
- Cloud frontier API: $3-15/1M tokens
- 80% of typical production traffic is extraction/classification/summarisation
- Route that 80% to the local model: your blended cost drops from ~$10 to ~$0.50/1M

But "just use the cheap model" is not a strategy. Quality varies. Provider updates break things. So the router samples 5% of cheap-model responses against a reference model in a background loop. If quality drops on any task category, that category auto-promotes to the next tier. You get the savings by default with an automatic safety net.

What else it does:

- Failover chains: if backend A is down, try B, then C. No code changes.
- Batch API routing: bulk work auto-submits to provider batch endpoints for 50% off (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks all offer this).
- Response caching: identical deterministic requests (temp=0) served from cache. 30% hit rate on extraction workloads.
- Per-service daily budgets: set a $10/day limit. When it is hit, the router downgrades to a cheaper model instead of failing.
- LoRA adapter routing: if your vLLM instance serves multiple fine-tuned adapters, the router sends each request to the right one.
- MCP server built in: Claude Code, Cursor, and Claude Desktop can manage backends, costs, and rules conversationally.
- Embedded web dashboard, Prometheus metrics, hot-reloadable YAML config.

Install:

```
curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | bash
kronaxis-router init    # auto-detects Ollama, vLLM, cloud API keys
kronaxis-router         # start
```

One command detects your local models and API keys, generates a config, and you are routing. Supports Ollama, vLLM, OpenAI, Gemini. OpenAI-compatible API so existing code just changes one URL.

**How this compares to alternatives:**

I looked at everything before building this. Here is where each option sits:

**LiteLLM** (Python, open source) -- the closest alternative. Supports 100+ providers, virtual API keys, spend tracking UI. If you need broad provider coverage and are already running Python infrastructure, LiteLLM is mature and well-maintained. Where it falls short: it is a Python process (300MB+ memory, ~2K req/s vs our 22K), it does not do intelligent cost-based routing (you pick the model per request, it does not classify and route for you), no quality validation loop, no batch API aggregation across providers, no response caching, no LoRA-aware routing. LiteLLM is a universal gateway. Kronaxis Router is a cost optimiser.

**OpenRouter** (SaaS) -- zero setup, huge model catalogue. But they add a margin on every request (typically 5-20% on top of provider pricing), your data goes through their servers, and you cannot route to local models. If cost reduction is the goal, adding a middleman margin is the opposite direction.

**Portkey** (SaaS) -- strong on observability, guardrails, prompt management. Good if you need those features. Not focused on cost-based routing. Paid plans start at $99/month. Your data goes through their infrastructure.

**Martian / Not Diamond** (ML-based routing) -- these use trained classifiers to pick models per request. More sophisticated routing than rule-based. But both are SaaS with usage-based pricing, closed source, and add latency for the ML classification step. Our classifier runs in under 1ms with zero network calls.

**The honest summary:** if you need 100+ provider support and do not care about intelligent routing, use LiteLLM. If you want zero setup and do not care about cost, use OpenRouter. If you want to minimise LLM spend with automatic quality validation, self-host on your own infrastructure, and run at 22K req/s on 2MB of RAM, this is built for that.

Single binary. 81 tests. 2MB memory under load. Apache 2.0.

GitHub: https://github.com/Kronaxis/kronaxis-router

Happy to answer questions about the routing heuristics, quality validation loop, or batch API integration.
