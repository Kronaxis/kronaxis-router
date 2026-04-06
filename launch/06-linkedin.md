# LinkedIn Post

If your team runs LLM workloads, you are almost certainly overspending.

Not because your provider is expensive. Because you are routing every request to the same model regardless of task complexity.

Most LLM traffic in production systems is structured extraction, summarisation, and template-based generation. A 9B parameter model handles these tasks with equivalent output quality to a 27B or a cloud frontier model. The hard tasks (multi-step reasoning, long-context synthesis, ambiguous instructions) genuinely need the larger model. But they are typically 15-20% of total volume.

The analogy I keep coming back to: a CFO can fill in accounts receivable, but a bookkeeper is 50x cheaper and does the job just as well. You would not pay CFO rates for bookkeeping. Yet that is exactly what happens when every API call hits the same expensive endpoint.

We built Kronaxis Router to solve this for our own infrastructure, and we have open-sourced it under Apache 2.0.

It is a single Go binary that sits between your applications and your model backends. It auto-classifies each incoming request, routes to the cheapest capable model tier, validates quality on a sampling basis, and auto-promotes requests if the cheap model's output degrades. Per-service cost budgets enforce spending limits with automatic downgrade.

For engineering leaders managing LLM costs: this is not a rip-and-replace. It drops in front of your existing OpenAI-compatible endpoints. Configuration is one YAML file. Prometheus metrics feed into your existing monitoring. The embedded dashboard gives finance and engineering a shared view of per-service, per-model spend.

In our production environment, 80% of traffic moved to the cheap tier with no quality regression. Inference costs dropped accordingly.

70 tests. Python and TypeScript SDKs. Helm chart for Kubernetes deployments.

GitHub: https://github.com/kronaxis/kronaxis-router
