# agent-gateway

OpenAI-compatible HTTP gateway that wraps CLI agents (Claude Code today, Gemini CLI as a real adapter, Anthropic SDK for cheap inference). Lives next to `kronaxis-router` and is fronted by it as a regular `type: openai` backend.

**Port:** 8055
**Location:** `kronaxis-router/agent-gateway/`
**Design:** [../../docs/plans/2026-05-08-agent-gateway-design.md](../../docs/plans/2026-05-08-agent-gateway-design.md)
**Plan:** [../../docs/plans/2026-05-08-agent-gateway-implementation-plan.md](../../docs/plans/2026-05-08-agent-gateway-implementation-plan.md)

## Build + run

```bash
cd kronaxis-router/agent-gateway
go mod tidy
go build -o agent-gateway .
./agent-gateway -config config.yaml
```

Or via systemd:

```bash
sudo cp agent-gateway.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now agent-gateway
```

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/chat/completions` | OpenAI-compatible. Stream + buffered. Proper `tool_calls` array. |
| GET | `/v1/models` | List adapters + availability |
| POST | `/v1/workspaces` | Create a persistent named workspace (multi-turn) |
| GET | `/v1/workspaces` | List workspaces |
| GET | `/v1/workspaces/{id}` | Workspace detail + git diff |
| DELETE | `/v1/workspaces/{id}` | Delete a workspace |
| GET | `/v1/accounts` | List configured auth-pool accounts + health |
| GET | `/healthz` | Liveness (checks `claude` binary) |
| GET | `/metrics` | Prometheus text-format metrics |
| GET | `/api/live` | SSE feed of recent request records (used by /) |
| GET | `/` | Live web UI dashboard |

## Models / adapters

| Model ID | Adapter | What it does |
|---|---|---|
| `claude-code-agent` | claude-cli | Spawns `claude -p --output-format stream-json` in an ephemeral worktree. Full tools, skills, MCP, hooks. Pro/Max sub via host's `claude auth login` or `ANTHROPIC_API_KEY`. |
| `claude-code` | claude-cli | Alias |
| `claude-sdk-agent` | anthropic-sdk | Direct POST to api.anthropic.com /v1/messages. Pure inference. No tools. Cheap path. Auth via API key. |
| `gemini-cli-agent` | gemini-cli | Spawns `gemini --prompt`, streams stdout as text. Real if `gemini` is on PATH. |

Append `+skillname` to any model id to invoke a Claude Code slash-command:

```
"model": "claude-code-agent+brainstorming"
```

The first user message is prefixed with `/skillname`.

## OpenAI request extensions

All non-standard. OpenAI clients ignore them; we use them.

```jsonc
{
  "model": "claude-code-agent",
  "stream": true,
  "messages": [{"role": "user", "content": "..."}],

  "system_prompt":         "you are a careful editor",
  "append_system_prompt":  "use British spelling",
  "agent":                 "Plan",
  "permission_mode":       "bypassPermissions",
  "claude_model":          "claude-sonnet-4-6",
  "effort":                "high",
  "allowed_tools":         ["Read", "Edit"],
  "disallowed_tools":      ["Bash"],
  "mcp_config":            "/path/to/mcp.json",
  "add_dirs":              ["/path/to/extra"],
  "bare":                  false,
  "skill":                 "brainstorming",
  "workspace_id":          "ws_abc123",
  "base_repo":             "/path/to/seed/repo",
  "include_hook_events":   false,
  "account_id":            "anthropic-1"
}
```

## Streaming response shape

Standard OpenAI SSE chunks. Tool calls appear as proper `tool_calls` deltas:

```
data: {"choices":[{"delta":{"role":"assistant"}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"toolu_...","type":"function","function":{"name":"Write","arguments":""}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file_path\":\""}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"hello.go\"}"}}]}}]}
data: {"choices":[{"delta":{"content":"Created hello.go"}}]}
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: {"kronaxis":{"workspace_path":"...","git_diff":"...","num_turns":2,"adapter":"claude-cli","duration_ms":4200,"workspace_id":"ws_..."}}
data: [DONE]
```

A `:keepalive` SSE comment is emitted every `keepalive_seconds` (default 15s) to keep idle connections alive through proxies.

## Persistent workspaces

```bash
# create
curl -X POST http://localhost:8055/v1/workspaces -d '{"created_by":"me"}'
# → {"id":"abc123","path":"...","created_at":"..."}

# call repeatedly with the same workspace_id
curl http://localhost:8055/v1/chat/completions -d '{
  "model":"claude-code-agent",
  "workspace_id":"abc123",
  "messages":[{"role":"user","content":"create note.txt"}]
}'
curl http://localhost:8055/v1/chat/completions -d '{
  "model":"claude-code-agent",
  "workspace_id":"abc123",
  "messages":[{"role":"user","content":"now read note.txt and tell me what it says"}]
}'

# delete
curl -X DELETE http://localhost:8055/v1/workspaces/abc123
```

The workspace TTL sweeper runs every `sweep_interval_minutes` and reaps workspaces with `mtime` older than `workspace_max_age_hours` (defaults: 30 min / 24 hours). `Touch` updates the mtime on each use.

## Multi-account auth pool

Scale past per-account limits by configuring multiple credentials per provider. The gateway round-robins; pinning by `account_id` per-request is supported. When an account hits a rate limit / 429 / auth error / billing failure, the pool automatically disables it for a provider-aware cooldown (5 minutes for API keys, **5 hours for Claude Code OAuth subscriptions** which reset on a 5-hour window).

### Two flavours: API keys vs OAuth subscriptions

| Provider type | Auth | Cooldown on rate-limit | ToS for pooled use |
|---|---|---|---|
| `anthropic` | API key | 5 min | ✓ commercial-OK |
| `openai` | API key | 5 min | ✓ commercial-OK |
| `gemini` | API key | 5 min | ✓ commercial-OK |
| `claude-cli` | OAuth subscription (Pro/Max) | 5 hours | personal use only |

### API-key pool (anthropic-sdk / openai / gemini)

`auth_pool.yaml`:

```yaml
accounts:
  - id: anthropic-1
    provider: anthropic
    api_key_env: ANTHROPIC_API_KEY_1
    notes: "Production tier"
  - id: anthropic-2
    provider: anthropic
    api_key_env: ANTHROPIC_API_KEY_2
  - id: openai-1
    provider: openai
    api_key_env: OPENAI_API_KEY_1
```

### Claude Code OAuth subscription pool (PERSONAL USE ONLY)

⚠️ **Anthropic ToS:** Pooling multiple consumer Claude Pro/Max subscriptions is appropriate ONLY for individual personal use. For multi-tenant SaaS, team-shared deployments, or commercial resale, use the `anthropic-sdk` adapter with paid API keys instead. The gateway enforces this with a one-time setup gate per account.

Add each subscription via the `claude-login` subcommand. It runs `claude auth login` with `CLAUDE_CONFIG_DIR` set to a per-account directory, captures the resulting `.credentials.json`, and writes a `.personal-use-acknowledged` marker that the auth-pool loader requires:

```bash
agent-gateway claude-login --name sub-1 --notes "Max account A"
# Interactive: type PERSONAL-USE-ONLY to confirm. Then `claude auth login`
# runs in a per-account config dir. Outputs the YAML stanza to add.
```

Default per-account dir: `~/.config/agent-gateway/accounts/<name>/`. Resulting auth-pool stanza:

```yaml
- id: sub-1
  provider: claude-cli
  claude_credentials_path: ~/.config/agent-gateway/accounts/sub-1/.credentials.json
  window_duration: 5h
  window_max_usd: 200          # optional; route around accounts that have consumed this $-equivalent
  notes: "Max account A"
```

Per-request, `agent-gateway` symlinks the chosen account's `.credentials.json` into the per-request `CLAUDE_CONFIG_DIR` so the spawned `claude` process uses that subscription.

### Status + USD-equivalent tracking

`GET /v1/accounts` returns each account's enabled/available state, plus rolling-window stats:

```json
{
  "id": "sub-1",
  "provider": "claude-cli",
  "enabled": true,
  "available": true,
  "success_count": 42,
  "failure_count": 0,
  "window_duration_seconds": 18000,
  "window_resets_at": "2026-05-08T20:23:00Z",
  "window_requests": 42,
  "window_tokens_total": 314000,
  "window_usd_equiv": 12.4321,
  "window_max_usd": 200,
  "window_pct_consumed": 6.21,
  "lifetime_input_tokens": 152000,
  "lifetime_output_tokens": 162000
}
```

`window_usd_equiv` is the API-equivalent dollar figure the same usage would have cost on the Anthropic API (taken from claude CLI's `total_cost_usd`). Lets you compare subscription value vs. paid API: a $200/mo Max account whose `window_usd_equiv` consistently exceeds `200/30/4.8` per 5-hour window is producing more than its monthly equivalent in API value, i.e. the subscription is paying off. Optional `window_max_usd` makes the pool route around an account once its window's $-equivalent crosses a threshold.

### Reset / removal

Delete `~/.config/agent-gateway/accounts/<name>/` to remove an account. Re-run `claude-login` to refresh credentials when OAuth tokens expire.

## Configuration

`config.yaml`:

```yaml
port: 8055
claude_binary: "claude"
gemini_binary: "gemini"

workspace_root: "/tmp/kx-agent"
base_repo: ""
retain_workspaces: false

timeout_seconds: 600
max_concurrent: 4
keepalive_seconds: 15

workspace_max_age_hours: 24
sweep_interval_minutes: 30

warm_pool_size: 2

audit_file: ""

anthropic_base_url: "https://api.anthropic.com"
anthropic_api_key_env: "ANTHROPIC_API_KEY"

auth_pool_file: ""
```

Env overrides: `AGENT_GATEWAY_PORT`, `AGENT_GATEWAY_CLAUDE_BIN`, `AGENT_GATEWAY_WORKSPACE_ROOT`, `AGENT_GATEWAY_AUDIT_FILE`.

## Observability

- **Audit log**: every request emits one JSON line to stderr (or `audit_file`). Lifecycle events (startup, sweep, workspace create/delete) too.
- **Prometheus metrics** at `/metrics`:
  - `agent_gateway_requests_total{adapter,model,status}` (counter)
  - `agent_gateway_request_errors_total{adapter,model,status}` (counter)
  - `agent_gateway_request_duration_ms{adapter,model,status}` (histogram)
  - `agent_gateway_tool_calls_total{tool}` (counter)
  - `agent_gateway_cost_usd_total{adapter}` (counter)
  - `agent_gateway_turns_total{adapter}` (counter)
  - `agent_gateway_active_requests` (gauge)
  - `agent_gateway_uptime_seconds` (gauge)
- **Live UI** at `/`: dashboard with totals + last-50 request rows, fed via `/api/live` SSE.

## Wire into kronaxis-router

Uncomment the `claude-code-agent` stanza in `kronaxis-router/config.yaml` (already present, commented):

```yaml
- name: claude-code-agent
  url: "http://localhost:8055"
  type: openai
  model_name: "claude-code-agent"
  cost_input_1m: 0.0
  cost_output_1m: 0.0
  capabilities: [json_output, tools, agentic, file_edit, long_context]
  max_concurrent: 4
  health_endpoint: "/healthz"
```

## Caveats

- **ToS posture**: claude-cli adapter uses host's `claude auth login` (Max sub). Internal-only deployment. Use the anthropic-sdk adapter (API key) when this becomes external.
- **Skills with interactive flow** (e.g. `/brainstorming`'s `AskUserQuestion`) won't fully work in `-p` headless mode. Skills that just do work (graphify, finishing-dev-branch, simplify) work fine.
- **Concurrency**: `max_concurrent` caps parallel `claude` subprocesses. Cold start ~3s per request without warm pool, ~0.3s with `warm_pool_size: 2`.
- **`gemini-cli` adapter is shallow**: it streams stdout as text deltas without tool-call extraction. Good enough for prompt-and-response use; not for multi-tool agentic work. Upgrade when Gemini CLI's structured output stabilises.

## Tests

```bash
go test ./...
```

Tests cover the stream-json parser (text + tool_calls + arguments accumulation + done event), `splitModelSkill`, and `applySkillPrefix`.
