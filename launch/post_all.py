#!/usr/bin/env python3
"""
Post Kronaxis Router launch content to Reddit, Twitter, and LinkedIn.
Connects to your RUNNING Chrome via CDP (no need to close Chrome).
Each post is filled in and paused for you to review and click submit.

Prerequisites: Chrome must be running with remote debugging.
If not already, this script restarts Chrome with --remote-debugging-port=9222.
"""

import subprocess
import time
from pathlib import Path
from playwright.sync_api import sync_playwright

SIGNAL_FILE = Path("/tmp/kronaxis-post-continue")
CDP_PORT = 9222

# ─── Post content ───────────────────────────────────────────────────

REDDIT_LOCALLLAMA_TITLE = "Source-released an LLM router that auto-classifies prompts and sends them to the cheapest model that can handle the task (Go, BSL 1.1)"
REDDIT_LOCALLLAMA_BODY = """Small models got good. Qwen 9B, Llama 8B, Gemma 4B handle 80% of production LLM workloads with output quality indistinguishable from frontier APIs. The problem is nobody routes. Everything hits the same endpoint.

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

Blog with cost arithmetic and full comparison: https://kronaxis.co.uk/blog/llm-routing-cost-savings"""

REDDIT_SELFHOSTED_TITLE = "Kronaxis Router: self-hosted LLM proxy that auto-routes to the cheapest capable model (single Go binary, 2MB RAM, no cloud needed, BSL 1.1)"
REDDIT_SELFHOSTED_BODY = """If you run local LLM inference (Ollama, vLLM, llama.cpp) you've probably thought about running multiple model sizes and routing between them. Small model for easy tasks, large model for hard ones.

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

81 tests. BSL 1.1. No telemetry, no phoning home.

GitHub: https://github.com/Kronaxis/kronaxis-router"""

LINKEDIN_BODY = """If your team runs LLM workloads, you are almost certainly overspending. Not because your provider is expensive. Because you are routing every request to the same model regardless of task complexity.

Here is what changed: small open-weight models (Qwen 9B, Llama 8B, Gemma 4B) now match frontier APIs on 80% of production tasks. Extraction, classification, summarisation, tagging. A 9B does them identically to GPT-4.

The remaining 20% genuinely needs the larger model. Multi-step reasoning, code generation, long-context synthesis. But those are 20%, not 100%.

The cost arithmetic: routing 80% of traffic from a $3-15/1M token API to a local model at $0.005/1M drops your blended cost from roughly $10 to $0.50 per million tokens.

We built Kronaxis Router to solve this for our own infrastructure, and we have source-released it under BSL 1.1.

Single Go binary. Sits between your applications and your model backends. Auto-classifies each request, routes to the cheapest capable tier, validates quality on a sampling basis, and auto-promotes if the cheap model degrades.

How it compares to alternatives:
→ LiteLLM: universal gateway (100+ providers), but no auto-routing, no quality validation, no caching. Python, 300MB+. Different tool.
→ OpenRouter: zero setup, but adds 5-20% margin. Wrong direction for cost reduction.
→ Portkey: strong on observability. $99+/month. Not focused on cost routing.

81 tests. 22K req/s. 2MB memory. BSL 1.1.

GitHub: https://github.com/Kronaxis/kronaxis-router

Full cost analysis and comparison: https://kronaxis.co.uk/blog/llm-routing-cost-savings

#LLM #OpenSource #MachineLearning #CostOptimization #DevTools #AI"""

TWEETS = [
    "Small LLMs got good. Qwen 9B, Llama 8B, Gemma 4B handle 80% of production workloads identically to frontier APIs.\n\nSo why is everyone still routing every request to GPT-4?\n\nWe source-released a fix. Thread 🧵",
    "The economics are brutal:\n\n• Local 9B: ~$0.005/1M tokens\n• Cloud frontier: $3-15/1M tokens\n\n80% of production LLM traffic is extraction, classification, summarisation. A 9B handles all of it.\n\nRoute that 80% locally: blended cost drops from ~$10 to ~$0.50/1M.",
    "Kronaxis Router: single Go binary, sits between your app and your models.\n\nAuto-classifies each request (<1ms, no LLM call) and routes to the cheapest capable backend.\n\nQuality validation loop samples 5% of cheap-model output. If quality drops, auto-promotes. Safety net built in.",
    "Also:\n• Failover chains across backends\n• Batch API routing: 50% off on 7 providers\n• Response caching (30% hit rate on extraction)\n• Per-service daily budgets (downgrade, don't fail)\n• LoRA adapter routing\n• MCP server for Claude Code / Cursor\n• Prometheus metrics + web dashboard",
    "vs LiteLLM: gateway vs cost optimiser. No auto-routing, no quality validation, no caching, Python (300MB) vs Go (2MB).\n\nvs OpenRouter: adds margin per request. Wrong direction for cost cutting.\n\nSingle binary. 81 tests. BSL 1.1.\n\ngithub.com/Kronaxis/kronaxis-router",
    "Full blog post with cost arithmetic, comparison tables, and install guide:\n\nkronaxis.co.uk/blog/llm-routing-cost-savings\n\nInstall in 30 seconds:\ncurl -fsSL .../install.sh | bash\nkronaxis-router init\nkronaxis-router\n\nOne command. Auto-detects Ollama, vLLM, cloud API keys.",
]


def wait_for_user(msg="review and submit"):
    """Wait for the user to touch the signal file."""
    SIGNAL_FILE.unlink(missing_ok=True)
    print(f"  >>> {msg}")
    print(f"  >>> Then run:  touch {SIGNAL_FILE}")
    while not SIGNAL_FILE.exists():
        time.sleep(0.5)
    SIGNAL_FILE.unlink(missing_ok=True)
    print("  Continuing...\n")


def ensure_cdp():
    """Check if Chrome is listening on CDP port, if not restart it."""
    import socket
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.connect(("127.0.0.1", CDP_PORT))
        s.close()
        return  # Already listening
    except ConnectionRefusedError:
        pass

    print(f"  Chrome not listening on port {CDP_PORT}. Restarting with remote debugging...")
    subprocess.run(["pkill", "-f", "chrome"], capture_output=True)
    time.sleep(2)
    subprocess.Popen([
        "/opt/google/chrome/chrome",
        f"--remote-debugging-port={CDP_PORT}",
        "--user-data-dir=~/.config/google-chrome",
        "--no-first-run",
        "--restore-last-session",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(4)
    print("  Chrome restarted with CDP enabled.")


def fill_reddit(page, url, title, body):
    """Open Reddit submit page, fill title and body."""
    page.goto(url, wait_until="domcontentloaded")
    page.wait_for_timeout(3000)

    try:
        # Title input
        title_el = page.locator('textarea[name="title"], input[name="title"], [placeholder*="Title"]').first
        title_el.wait_for(timeout=10000)
        title_el.click()
        title_el.fill(title)
        page.wait_for_timeout(500)

        # Try markdown mode
        try:
            md_btn = page.locator('button:has-text("Markdown Mode"), button:has-text("Switch to markdown")').first
            if md_btn.is_visible(timeout=2000):
                md_btn.click()
                page.wait_for_timeout(500)
        except:
            pass

        # Body textarea
        body_el = page.locator('textarea[name="body"], textarea[slot="body"], div[contenteditable="true"][class*="text"], textarea[placeholder*="body"]').first
        body_el.wait_for(timeout=8000)
        body_el.click()
        body_el.fill(body)
        print("  ✓ Title and body filled.")
    except Exception as e:
        # Fallback: copy to clipboard
        subprocess.run(["xclip", "-selection", "clipboard"], input=body.encode(), check=False)
        print(f"  Auto-fill partial ({e})")
        print(f"  Body copied to clipboard. Ctrl+V to paste.")


def fill_linkedin(page, body):
    """Open LinkedIn, click start a post, fill body."""
    page.goto("https://www.linkedin.com/feed/", wait_until="domcontentloaded")
    page.wait_for_timeout(3000)

    try:
        start_btn = page.locator('button:has-text("Start a post")').first
        start_btn.wait_for(timeout=8000)
        start_btn.click()
        page.wait_for_timeout(2000)

        editor = page.locator('[contenteditable="true"][role="textbox"], .ql-editor, [data-placeholder]').first
        editor.wait_for(timeout=5000)
        editor.click()
        page.keyboard.type(body, delay=1)
        print("  ✓ Post body filled.")
    except Exception as e:
        subprocess.run(["xclip", "-selection", "clipboard"], input=body.encode(), check=False)
        print(f"  Auto-fill failed ({e}). Body copied to clipboard. Click 'Start a post', Ctrl+V.")


def fill_tweet(page, tweet_text, is_first=True):
    """Fill a tweet in the Twitter composer."""
    if is_first:
        page.goto("https://x.com/compose/post", wait_until="domcontentloaded")
        page.wait_for_timeout(3000)

    try:
        editor = page.locator('[contenteditable="true"][role="textbox"], [data-testid="tweetTextarea_0"]').first
        editor.wait_for(timeout=8000)
        editor.click()
        page.keyboard.type(tweet_text, delay=2)
        print("  ✓ Tweet filled.")
    except Exception as e:
        subprocess.run(["xclip", "-selection", "clipboard"], input=tweet_text.encode(), check=False)
        print(f"  Auto-fill failed ({e}). Copied to clipboard. Ctrl+V.")


def main():
    print("=" * 60)
    print("  Kronaxis Router Launch Poster")
    print("  Connects to your running Chrome via CDP")
    print("  Each form filled, then PAUSED for you to submit")
    print("=" * 60)
    print()

    ensure_cdp()

    with sync_playwright() as p:
        browser = p.chromium.connect_over_cdp(f"http://127.0.0.1:{CDP_PORT}")
        context = browser.contexts[0]  # Use existing context with cookies

        # 1. Reddit r/LocalLLaMA
        print("[1/4] Reddit r/LocalLLaMA")
        page = context.new_page()
        fill_reddit(page, "https://www.reddit.com/r/LocalLLaMA/submit?type=TEXT",
                    REDDIT_LOCALLLAMA_TITLE, REDDIT_LOCALLLAMA_BODY)
        wait_for_user("Review and click Post on r/LocalLLaMA")

        # 2. Reddit r/selfhosted
        print("[2/4] Reddit r/selfhosted")
        page2 = context.new_page()
        fill_reddit(page2, "https://www.reddit.com/r/selfhosted/submit?type=TEXT",
                    REDDIT_SELFHOSTED_TITLE, REDDIT_SELFHOSTED_BODY)
        wait_for_user("Review and click Post on r/selfhosted")

        # 3. LinkedIn
        print("[3/4] LinkedIn")
        page3 = context.new_page()
        fill_linkedin(page3, LINKEDIN_BODY)
        wait_for_user("Review and click Post on LinkedIn")

        # 4. Twitter thread
        print("[4/4] Twitter/X thread (6 tweets)")
        page4 = context.new_page()
        for i, tweet in enumerate(TWEETS):
            print(f"  Tweet {i+1}/{len(TWEETS)}: {tweet[:50]}...")
            fill_tweet(page4, tweet, is_first=(i == 0))
            wait_for_user(f"Post tweet {i+1}, then click reply for the next one")
            if i < len(TWEETS) - 1:
                page4.goto("https://x.com/compose/post", wait_until="domcontentloaded")
                page4.wait_for_timeout(2000)

        print()
        print("=" * 60)
        print("  All 4 platforms done!")
        print("  r/MachineLearning: post tomorrow")
        print("=" * 60)


if __name__ == "__main__":
    main()
