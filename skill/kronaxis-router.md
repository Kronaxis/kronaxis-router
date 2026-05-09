---
name: kronaxis-router
description: >
  Manage LLM routing, costs, and backends via the Kronaxis Router.
  Use when the user asks about LLM costs, backend health, routing rules,
  or wants to add/remove/configure LLM backends.
  Triggers on: "router", "LLM costs", "backend health", "routing rules",
  "add backend", "add model", "switch model", "LLM budget".
tools:
  - router_health
  - router_backends
  - router_costs
  - router_stats
  - router_rules
  - router_add_backend
  - router_remove_backend
  - router_add_rule
  - router_remove_rule
  - router_update_budget
  - router_config
  - router_reload
---

# Kronaxis Router Skill

You have access to the Kronaxis Router MCP tools for managing LLM routing and costs.

## Available Tools

| Tool | Purpose |
|------|---------|
| `router_health` | Check overall router health: backend statuses, uptime, cache stats |
| `router_backends` | List all LLM backends with health, type, URL, latency, costs |
| `router_costs` | View LLM spending by service/model/call_type (today/week/month) |
| `router_stats` | Live request metrics: total, active, errors, latency, breakdowns |
| `router_rules` | List all routing rules with priority, match criteria, backends |
| `router_add_backend` | Register a new LLM endpoint (vLLM, Ollama, Gemini, OpenAI) |
| `router_remove_backend` | Remove a backend by name |
| `router_add_rule` | Create a routing rule (match on service/tier/priority/call_type) |
| `router_remove_rule` | Remove a rule by name |
| `router_update_budget` | Set daily spending limits per service (downgrade or reject on exceed) |
| `router_config` | View the full YAML config |
| `router_reload` | Force config reload from disk |

## How Routing Works

Requests arrive at the router's OpenAI-compatible endpoint. The router:
1. Extracts metadata from `X-Kronaxis-*` headers (service, call_type, tier, priority)
2. Evaluates rules in priority order (highest first)
3. Filters backends by health, capabilities, LoRA adapters, and cost ceiling
4. Forwards to the first healthy, capable backend
5. Falls back to the next backend on failure

### Key Concepts

- **Tier 1** = heavy reasoning (plan, analyse, create) -> route to capable model
- **Tier 2** = light extraction (classify, parse, score) -> route to cheap model
- **Priority**: interactive (skip batching), normal, background (batched), bulk (batch API, 50% off)
- **Budget action**: "downgrade" routes to a cheaper model; "reject" returns 429

## Common Tasks

### Check what is going on
Use `router_health` for overall status, `router_backends` for per-backend detail.

### See spending
Use `router_costs` with period "today", "week", or "month".

### Add a local Ollama model
```
router_add_backend:
  name: "ollama-llama3"
  url: "http://localhost:11434"
  type: "ollama"
  model_name: "llama3.1:8b"
  cost_input_1m: 0
  cost_output_1m: 0
  max_concurrent: 4
```

### Add a cloud fallback
```
router_add_backend:
  name: "cloud-gemini"
  url: "https://generativelanguage.googleapis.com"
  type: "gemini"
  model_name: "gemini-2.5-flash"
  api_key: "env:GEMINI_API_KEY"
  cost_input_1m: 0.15
  cost_output_1m: 0.60
  max_concurrent: 50
```

### Route cheap work to the local model
```
router_add_rule:
  name: "local-extraction"
  priority: 150
  match:
    tier: 2
  backends: ["ollama-llama3", "cloud-gemini"]
```

### Set a daily budget
```
router_update_budget:
  service: "default"
  daily_limit_usd: 5.00
  action: "downgrade"
  downgrade_target: "ollama-llama3"
```

## Installation

If the router is not running, tell the user:

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | sh

# Auto-detect backends and generate config
kronaxis-router init

# Start
kronaxis-router

# Configure Claude Code MCP (one-time)
kronaxis-router init --claude
```
