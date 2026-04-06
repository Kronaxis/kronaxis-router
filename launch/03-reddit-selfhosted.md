# Reddit r/selfhosted Post

**Title:** Kronaxis Router: self-hosted LLM proxy that routes to the cheapest capable model (single Go binary, no cloud dependency, Apache 2.0)

**Body:**

If you are running local LLM inference (Ollama, vLLM, llama.cpp, text-generation-inference), you have probably thought about running multiple model sizes and routing between them. Small model for simple tasks, large model for hard ones.

Kronaxis Router does exactly that. Single Go binary, one YAML config file, no external dependencies, no cloud accounts required.

**What it does:**

- Sits between your apps and your model backends (any OpenAI-compatible API)
- Auto-classifies incoming prompts and routes to the appropriate model tier
- Failover: if your primary backend is down, retries on the next configured backend
- Response caching: deterministic requests get served from cache
- Per-service cost budgets: set a daily token budget per API key, auto-downgrades to the cheaper model when budget runs low
- Prometheus metrics endpoint for Grafana dashboards
- Embedded web UI for real-time monitoring

**Self-hosted specifics:**

- Single static binary. Download, chmod, run. No Python, no Node, no containers required (Docker image also available).
- Helm chart included if you run Kubernetes.
- Zero outbound network calls unless you configure cloud backends. Runs entirely on your LAN.
- The web dashboard is embedded in the binary. No separate frontend to deploy.
- Config hot-reload: edit the YAML, changes apply in 5 seconds. No restart needed.

**If you also use cloud APIs:**

The router supports 7 cloud providers (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks) as fallback or overflow backends. Batch API support gets you roughly 50% off on eligible requests. But cloud is entirely optional.

70 tests. Apache 2.0. No telemetry, no phoning home.

GitHub: https://github.com/kronaxis/kronaxis-router
