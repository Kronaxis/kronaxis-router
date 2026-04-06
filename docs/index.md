---
layout: default
title: Home
---

# Kronaxis Router

**Intelligent LLM proxy that routes every request to the cheapest model capable of delivering the required output.**

A CFO can fill in accounts receivable, but a bookkeeper is 50x cheaper and does the job just as well. Kronaxis Router applies this principle to LLM inference.

## Key Features

| Feature | Benefit |
|---------|---------|
| **Cost-optimised routing** | YAML rules route structured extraction to cheap models, heavy reasoning to powerful models |
| **Automatic tier classification** | Prompt analysis auto-assigns tier. No caller changes needed. |
| **Backend failover** | If the first backend fails, automatically tries the next. Retry with backoff. |
| **50% off batch API** | Auto-routes bulk work to provider batch APIs (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks) |
| **Response caching** | Identical deterministic requests served from cache. Zero backend calls. |
| **Quality validation** | Samples cheap-model output, verifies against reference model. Auto-promotes if quality drops. |
| **Per-service budgets** | Daily cost caps with automatic downgrade to cheaper models |
| **LoRA adapter routing** | Knows which vLLM instance has which adapters. Routes to the right one. |
| **Prometheus metrics** | Request counts, latency histograms, error rates, backend health |
| **Embedded web UI** | Dashboard, visual flow builder, backend manager, cost analysis |
| **A/B testing** | Split traffic between models, compare quality and cost |
| **Audit log** | PII-redacted request/response logging for compliance |

## Quick Start

```bash
go build -o kronaxis-router .
./kronaxis-router
# Open http://localhost:8050
```

Point your services at `http://localhost:8050/v1/chat/completions` instead of calling LLM backends directly.

## Documentation

- [Installation Guide](installation.md)
- [User Guide](user-guide.md)
- [Configuration Reference](configuration.md)
- [API Reference](api-reference.md)
- [Deployment Guide](deployment.md)
- [Monitoring Guide](monitoring.md)
- [Architecture](architecture.md)

## SDKs

- **Python**: `pip install kronaxis-router` -- [SDK docs]({{ site.baseurl }}/sdks/python/)
- **TypeScript**: `npm install kronaxis-router` -- [SDK docs]({{ site.baseurl }}/sdks/typescript/)

## Licence

Apache 2.0. Built by [Kronaxis](https://kronaxis.co.uk).
