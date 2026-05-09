#!/bin/bash
# Launch poster: opens each platform in your existing Chrome,
# copies the body to clipboard. You paste and submit.
# Run: bash launch/post_all.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "════════════════════════════════════════════════"
echo "  Kronaxis Router Launch Poster"
echo "  Opens each platform, copies body to clipboard"
echo "  You: paste (Ctrl+V) and click submit"
echo "════════════════════════════════════════════════"
echo ""

# ─── 1. Reddit r/LocalLLaMA ───────────────────────────────────────

read -p "[1/4] Ready to post to r/LocalLLaMA? (Enter to open) " _

# Title goes to primary selection, body to clipboard
TITLE="Source-released LLM router that auto-classifies prompts and sends them to the cheapest model that can handle the task (Go, BSL 1.1)"
echo -n "$TITLE" | xclip -selection primary

cat << 'BODY' | xclip -selection clipboard
Small models got good. Qwen 9B, Llama 8B, Gemma 4B handle 80% of production LLM workloads with output quality indistinguishable from frontier APIs. The problem is nobody routes. Everything hits the same endpoint.

Kronaxis Router fixes this. Single Go binary, sits between your apps and your models, classifies each request, routes to the cheapest capable backend.

**How it works for local setups:**

The router auto-classifies each request. JSON extraction, entity tagging, summarisation: routed to your cheap model. Multi-step reasoning, code generation, long-context synthesis: routed to your large model. If you run LoRA adapters on vLLM with `--enable-lora`, the router handles adapter selection per request.

**Quality validation loop:**

Every N requests, the router sends the same prompt to both the cheap and reference model. If the cheap model's quality drops below threshold, that task category auto-promotes. No manual intervention. Savings by default, automatic safety net.

**What this changed for us:**

Before: 100% of traffic hitting the 27B. Slow, GPU-bound, queue backing up.

After: 80% on the 9B (3x faster inference, half the VRAM), 20% on the 27B where it actually matters. Same output quality. The 27B queue cleared completely.

**Install:**

    curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | bash
    kronaxis-router init    # auto-detects Ollama, vLLM, cloud API keys
    kronaxis-router

**vs LiteLLM:** LiteLLM is a universal gateway (100+ providers). This is a cost optimiser. No auto-routing, no quality validation, no caching, no batch API, no LoRA routing. Python (300MB+, ~2K req/s) vs Go (2MB, 22K req/s). Different tools.

**vs OpenRouter:** Adds 5-20% margin per request, no local models. Wrong direction for cost reduction.

81 tests. BSL 1.1. Prometheus metrics, embedded dashboard, MCP server for Claude Code/Cursor.

GitHub: https://github.com/Kronaxis/kronaxis-router

Blog with cost arithmetic and full comparison: https://kronaxis.co.uk/blog/llm-routing-cost-savings
BODY

xdg-open "https://www.reddit.com/r/LocalLLaMA/submit?type=TEXT" 2>/dev/null
echo "  Title in middle-click paste, body in Ctrl+V"
echo "  1. Middle-click in title field (or type it)"
echo "  2. Switch to Markdown mode"
echo "  3. Click body field, Ctrl+V"
echo "  4. Submit"
read -p "  Done? (Enter to continue) " _

# ─── 2. Reddit r/selfhosted ───────────────────────────────────────

read -p "[2/4] Ready to post to r/selfhosted? (Enter to open) " _

cat << 'BODY' | xclip -selection clipboard
If you run local LLM inference (Ollama, vLLM, llama.cpp) you've probably thought about running multiple model sizes and routing between them. Small model for easy tasks, large model for hard ones.

Kronaxis Router does exactly that. Single Go binary, one YAML config, no external dependencies, no cloud accounts required.

**What it does:**

- Sits between your apps and your model backends (any OpenAI-compatible API)
- Auto-classifies prompts and routes to the appropriate model tier
- Failover: primary backend down, auto-retries on the next one
- Response caching: deterministic requests served from cache
- Per-service daily budgets: hit the limit, auto-downgrades to cheaper model instead of failing
- Prometheus metrics for Grafana dashboards
- Embedded web UI for monitoring

**Self-hosted specifics:**

- Single static binary. Download, chmod, run. No Python, no Node, no containers required.
- 2MB memory under full load. 22K req/s throughput.
- Zero outbound network calls unless you configure cloud backends. Runs entirely on your LAN.
- Web dashboard embedded in the binary. No separate frontend.
- Config hot-reload: edit the YAML, changes apply in 5 seconds. No restart.

**Install:**

    curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | bash
    kronaxis-router init    # auto-detects Ollama, vLLM on localhost
    kronaxis-router

Also available: `brew install kronaxis/tap/kronaxis-router`, `go install`, Docker, deb/rpm packages.

**If you also use cloud APIs:**

Supports 7 cloud providers as fallback. Batch API routing gets 50% off on eligible requests. But cloud is entirely optional.

81 tests. BSL 1.1. No telemetry, no phoning home.

GitHub: https://github.com/Kronaxis/kronaxis-router
BODY

TITLE2="Kronaxis Router: self-hosted LLM proxy that auto-routes to the cheapest capable model (single Go binary, 2MB RAM, no cloud needed, BSL 1.1)"
echo -n "$TITLE2" | xclip -selection primary

xdg-open "https://www.reddit.com/r/selfhosted/submit?type=TEXT" 2>/dev/null
echo "  Title in middle-click paste, body in Ctrl+V"
read -p "  Done? (Enter to continue) " _

# ─── 3. LinkedIn ──────────────────────────────────────────────────

read -p "[3/4] Ready to post to LinkedIn? (Enter to open) " _

cat << 'BODY' | xclip -selection clipboard
If your team runs LLM workloads, you are almost certainly overspending. Not because your provider is expensive. Because you are routing every request to the same model regardless of task complexity.

Here is what changed: small open-weight models (Qwen 9B, Llama 8B, Gemma 4B) now match frontier APIs on 80% of production tasks. Extraction, classification, summarisation, tagging. A 9B does them identically to GPT-4.

The remaining 20% genuinely needs the larger model. Multi-step reasoning, code generation, long-context synthesis. But those are 20%, not 100%.

The cost arithmetic: routing 80% of traffic from a $3-15/1M token API to a local model at $0.005/1M drops your blended cost from roughly $10 to $0.50 per million tokens.

We built Kronaxis Router to solve this for our own infrastructure, and we have released the source under BSL 1.1.

Single Go binary. Sits between your applications and your model backends. Auto-classifies each request, routes to the cheapest capable tier, validates quality on a sampling basis, and auto-promotes if the cheap model degrades.

How it compares to alternatives:
→ LiteLLM: universal gateway (100+ providers), but no auto-routing, no quality validation, no caching. Python, 300MB+. Different tool.
→ OpenRouter: zero setup, but adds 5-20% margin. Wrong direction for cost reduction.
→ Portkey: strong on observability. $99+/month. Not focused on cost routing.

81 tests. 22K req/s. 2MB memory. BSL 1.1.

GitHub: https://github.com/Kronaxis/kronaxis-router

Full cost analysis and comparison: https://kronaxis.co.uk/blog/llm-routing-cost-savings

#LLM #OpenSource #MachineLearning #CostOptimization #DevTools #AI
BODY

xdg-open "https://www.linkedin.com/feed/" 2>/dev/null
echo "  Body in Ctrl+V"
echo "  1. Click 'Start a post'"
echo "  2. Ctrl+V to paste"
echo "  3. Post"
read -p "  Done? (Enter to continue) " _

# ─── 4. Twitter/X ────────────────────────────────────────────────

read -p "[4/4] Ready to post Twitter thread? (Enter to open) " _

TWEETS=(
'Small LLMs got good. Qwen 9B, Llama 8B, Gemma 4B handle 80% of production workloads identically to frontier APIs.

So why is everyone still routing every request to GPT-4?

We released the source for a fix. Thread 🧵'

'The economics are brutal:

• Local 9B: ~$0.005/1M tokens
• Cloud frontier: $3-15/1M tokens

80% of production LLM traffic is extraction, classification, summarisation. A 9B handles all of it.

Route that 80% locally: blended cost drops from ~$10 to ~$0.50/1M.'

'Kronaxis Router: single Go binary, sits between your app and your models.

Auto-classifies each request (<1ms, no LLM call) and routes to the cheapest capable backend.

Quality validation loop samples 5% of cheap-model output. If quality drops, auto-promotes. Safety net built in.'

'Also:
• Failover chains across backends
• Batch API routing: 50% off on 7 providers
• Response caching (30% hit rate on extraction)
• Per-service daily budgets (downgrade, don'"'"'t fail)
• LoRA adapter routing
• MCP server for Claude Code / Cursor
• Prometheus metrics + web dashboard'

'vs LiteLLM: gateway vs cost optimiser. No auto-routing, no quality validation, no caching, Python (300MB) vs Go (2MB).

vs OpenRouter: adds margin per request. Wrong direction for cost cutting.

Single binary. 81 tests. BSL 1.1.

github.com/Kronaxis/kronaxis-router'

'Full blog post with cost arithmetic, comparison tables, and install guide:

kronaxis.co.uk/blog/llm-routing-cost-savings

Install in 30 seconds:
curl -fsSL .../install.sh | bash
kronaxis-router init
kronaxis-router

One command. Auto-detects Ollama, vLLM, cloud API keys.'
)

xdg-open "https://x.com/compose/post" 2>/dev/null

for i in "${!TWEETS[@]}"; do
    n=$((i + 1))
    echo -n "${TWEETS[$i]}" | xclip -selection clipboard
    echo "  Tweet $n/6 copied to clipboard. Ctrl+V, then post."
    read -p "  Posted? (Enter for next tweet) " _
done

echo ""
echo "════════════════════════════════════════════════"
echo "  All done!"
echo "  r/MachineLearning: post tomorrow"
echo "════════════════════════════════════════════════"
