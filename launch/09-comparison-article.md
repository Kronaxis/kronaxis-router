# Kronaxis Router vs LiteLLM vs Portkey

## Three tools, three problems

LLM proxy is an overloaded term. These three tools sit at different points in the stack.

**LiteLLM** normalises provider APIs. One interface, 100+ backends. Your code calls the same function whether the model is on OpenAI, Anthropic, or a local Ollama instance.

**Portkey** is a managed gateway. Logging, caching, fallback, guardrails, a dashboard. SaaS product with a generous free tier.

**Kronaxis Router** makes the routing decision. Given this specific prompt, which model tier should handle it? It auto-classifies, routes, validates quality, and enforces budgets.

## Comparison

| Feature | Kronaxis Router | LiteLLM | Portkey |
|---|---|---|---|
| **Primary value** | Cost-optimised routing | Provider normalisation | Observability + gateway |
| **Auto-classification** | Yes (<1ms, rule-based) | No | No |
| **Quality validation** | Yes (sampling + auto-promote) | No | No |
| **Per-service cost budgets** | Yes (with auto-downgrade) | Yes (enterprise) | Yes |
| **LoRA adapter routing** | Yes | No | No |
| **Provider support** | OpenAI-compatible only | 100+ providers | 200+ providers |
| **Response caching** | Built-in (in-memory) | Redis plugin | Built-in |
| **Batch API (50% off)** | Yes (7 providers) | Yes | Yes |
| **Deployment** | Single Go binary | Python (pip/Docker) | SaaS or self-hosted |
| **Licence** | Apache 2.0 | Apache 2.0 | Proprietary |

## When to use each

**Use LiteLLM when** you need a single API interface across many providers and your routing logic is handled elsewhere.

**Use Portkey when** you need managed observability with guardrails and a polished dashboard, and you are comfortable with SaaS.

**Use Kronaxis Router when** you run a mix of model sizes and want automatic cost optimisation with quality safety nets.

## Combining them

The most powerful setup:

```
Application --> Kronaxis Router --> LiteLLM --> Multiple providers
                    |
              (routing decision)
```

Router decides which tier. LiteLLM normalises the downstream providers within each tier.

## The honest assessment

Kronaxis Router is new and narrowly scoped. It does one thing well. LiteLLM has years of production hardening. Portkey has the best dashboard.

If you do not have multiple model tiers, Kronaxis Router adds no value. Pick the tool that solves the problem you actually have.

GitHub links:
- Kronaxis Router: https://github.com/kronaxis/kronaxis-router
- LiteLLM: https://github.com/BerriAI/litellm
- Portkey: https://github.com/portkey-ai/gateway
