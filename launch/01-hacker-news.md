# Hacker News Show HN Post

**Title:** Show HN: Kronaxis Router -- Open-source Go LLM proxy that routes to the cheapest capable model

**Body:**

I built an LLM proxy that auto-classifies incoming prompts and routes them to the cheapest model that can handle the task. A CFO can fill in accounts receivable, but a bookkeeper is 50x cheaper and does the job just as well. Same principle.

The problem: every team running LLM workloads ends up with the same mess. Extraction tasks hitting GPT-4 because nobody wrote the routing logic. Fallback code duplicated across six microservices. No visibility into which service is burning money or why.

Kronaxis Router sits between your application and your models. It classifies each request (structured extraction, summarisation, reasoning, code generation, creative writing) and routes to the appropriate tier. Simple extraction goes to a local 9B. Complex reasoning goes to a 27B or cloud API. If the cheap model fails quality validation, the request auto-promotes to the next tier.

Key features:

- Prompt auto-classification with configurable tier mapping
- Backend failover with retry across 7 providers (OpenAI, Anthropic, Gemini, Mistral, Groq, Together, Fireworks)
- 50% cost reduction on batch-eligible requests
- Response caching for deterministic prompts
- Quality validation: periodically samples cheap model output against a reference model and auto-promotes if quality degrades
- Per-service cost budgets with automatic downgrade
- LoRA adapter routing for multi-tenant fine-tuned model serving
- Prometheus metrics and an embedded web dashboard

Single Go binary. 70 tests. Apache 2.0.

We run this in production routing between local vLLM instances and cloud APIs. Our inference costs dropped from around $0.60/1M tokens to $0.03/1M for 80% of our traffic with no measurable quality loss on those tasks.

GitHub: https://github.com/kronaxis/kronaxis-router

Happy to answer questions about the routing heuristics or architecture.
