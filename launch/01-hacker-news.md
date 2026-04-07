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

Single binary. 81 tests. 2MB memory under load. Apache 2.0.

GitHub: https://github.com/Kronaxis/kronaxis-router

Happy to answer questions about the routing heuristics, quality validation loop, or batch API integration.
