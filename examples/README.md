# Example configurations

Copy-paste configs for common kronaxis-router deployments. Each file is a complete `config.yaml` that runs as-is once you fill in API keys / GPU URLs.

| File | What it does |
|---|---|
| [`vllm-only.yaml`](vllm-only.yaml) | Single local vLLM backend. No cloud, no fallback. Air-gapped or single-GPU dev. |
| [`cloud-only.yaml`](cloud-only.yaml) | Gemini + OpenAI only, no GPUs. Serverless / teams without GPU infrastructure. |
| [`hybrid.yaml`](hybrid.yaml) | Local vLLM + cloud failover. The most common setup. |
| [`agent-gateway-claude-code.yaml`](agent-gateway-claude-code.yaml) | **Wrap Claude Code as an OpenAI endpoint.** Talk to the actual `claude` CLI via `/v1/chat/completions`. |
| [`graphify-rag.yaml`](graphify-rag.yaml) | **RAG pre-stage with pgvector.** Compress fat prompts, augment thin ones, before any backend sees the request. |
| [`multi-account-pool.yaml`](multi-account-pool.yaml) | **Pool multiple Anthropic / OpenAI / Gemini API keys** with round-robin and auto-disable on 429. |
| [`oauth-subscription-pool.yaml`](oauth-subscription-pool.yaml) | **Pool multiple Claude Pro/Max OAuth subscriptions** for personal use. ToS-gated. |
| [`full-stack.yaml`](full-stack.yaml) | All features at once: cost routing + cache + batch + graphify + agent-gateway + auth pools. |

## Quick run

```bash
# Pick one, edit your API keys and backend URLs, then:
kronaxis-router serve --config examples/hybrid.yaml
```

Or with Docker:

```bash
docker run -p 8050:8050 -v $(pwd)/examples/hybrid.yaml:/app/config.yaml \
  ghcr.io/kronaxis/kronaxis-router:latest
```

## Common adjustments

- **Backend URLs**: replace `http://localhost:8000` (vLLM examples) with your actual server.
- **API keys**: each cloud backend needs `api_key: "env:NAME"` -- set the env var when running.
- **Per-service tuning**: routing rules match on `X-Kronaxis-Service` and other headers; rename them to your services.
- **Feature flags**: `graphify.enabled`, `agent_gateway`, `auth_pool_file` are all opt-in; turn them on when you're ready.

See the main [README](../README.md) for the full feature list and the rationale.
