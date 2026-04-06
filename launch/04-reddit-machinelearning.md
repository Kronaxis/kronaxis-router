# Reddit r/MachineLearning Post

**Title:** [P] Kronaxis Router: open-source Go proxy for cost-optimised LLM routing with auto-classification and quality validation

**Body:**

We open-sourced our LLM routing proxy. The core idea: most LLM workloads are a mix of easy and hard tasks, but teams route everything to the same expensive model because writing and maintaining routing logic is tedious.

**The auto-classification heuristic:**

The router classifies incoming prompts into task categories using a lightweight rule-based classifier (no LLM call for classification itself). Features extracted:

- Prompt structure: presence of JSON schema, output format constraints
- Token count and estimated output length
- Keyword signals: "extract", "summarise", "translate" vs "reason", "analyse", "compare"
- System prompt patterns: structured extraction templates vs open-ended instructions
- Temperature and max_tokens signals

Each task category maps to a model tier in config. The classifier adds less than 1ms of overhead. It is deliberately conservative: uncertain classifications route to the higher tier.

**Quality validation feedback loop:**

Configurable sampling rate (default 5%). For sampled requests, the router sends the same prompt to both the assigned model and a reference model. If the cheap model's quality on a task category drops below threshold over a sliding window, that category auto-promotes to the next tier. Promotions are logged and visible in the dashboard.

**Comparison to LiteLLM:**

LiteLLM normalises provider APIs behind a single interface. Kronaxis Router decides which model to call. They are complementary.

| Feature | Kronaxis Router | LiteLLM |
|---|---|---|
| Auto-classification routing | Yes | No |
| Quality validation loop | Yes | No |
| Per-service cost budgets | Yes | Yes (enterprise) |
| LoRA adapter routing | Yes | No |
| Provider API normalisation | OpenAI-compat only | 100+ providers |
| Response caching | Built in | Via Redis plugin |
| Language | Go (single binary) | Python |
| Licence | Apache 2.0 | Apache 2.0 |

70 tests. Python and TypeScript SDKs. Helm chart for Kubernetes.

GitHub: https://github.com/kronaxis/kronaxis-router
