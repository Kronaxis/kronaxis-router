# Reddit r/LocalLLaMA Post

**Title:** Open-sourced our LLM routing proxy: auto-classifies prompts and sends them to the cheapest model that can handle the task (Go, Apache 2.0)

**Body:**

We run a mix of local vLLM instances (9B and 27B quantised models) alongside cloud APIs for overflow. The routing logic kept getting duplicated across services, and we had no central view of what was costing what or why.

So we built Kronaxis Router. Single Go binary that sits in front of your model backends and makes the routing decision for you.

**How it works for local vLLM setups:**

The router auto-classifies each incoming request. JSON extraction, entity tagging, simple summarisation: routed to your cheap 9B. Multi-step reasoning, code generation, long-context synthesis: routed to your 27B. If you are running LoRA adapters on a single vLLM instance with `--enable-lora`, the router handles adapter selection per request based on task type or explicit header.

**Quality validation loop:**

Every N requests, the router sends the same prompt to both the cheap and reference model and compares outputs. If the cheap model's quality drops below your threshold, that task category auto-promotes to the next tier. No manual intervention.

**What changed for us:**

Before: 100% of traffic hitting the 27B. Slow, GPU-bound, queue backing up.

After: 80% of traffic on the 9B (3x faster inference, half the VRAM), 20% on the 27B where it actually matters. Same output quality on the tasks that moved down. The 27B queue cleared completely.

**LoRA routing:**

If you serve multiple LoRA adapters (we have 13), the router selects the right adapter based on request metadata. No client-side logic needed. The client sends a standard OpenAI-compatible request; the router rewrites the model field to the correct adapter name.

Config is a single YAML file. Prometheus metrics built in. Embedded web dashboard shows per-model, per-service cost and latency.

70 tests. Apache 2.0. Python and TypeScript SDKs included.

GitHub: https://github.com/kronaxis/kronaxis-router
