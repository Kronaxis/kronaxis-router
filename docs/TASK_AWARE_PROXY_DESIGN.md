# Task-Aware Proxy — Design Spec

**Status:** Design-only. Not implemented. Ready for a future session to pick up.
**Authored:** 2026-04-17 (session 84).
**Supersedes:** N/A (new feature).

---

## TL;DR

Today the router handles only `/v1/chat/completions` — one endpoint, one body format, OpenAI chat only. This means YOLO (:8890), captcha-solver (:9100), voice-agent (:8850), and every future GPU service (embeddings, TTS, diffusion, Whisper) bypass the router entirely, losing routing/failover/cost/audit/rate-limiting.

Add a single new public endpoint `/v1/task` that forwards arbitrary request bodies to a backend chosen by the existing rule engine, keyed on a new `X-Kronaxis-Task` header. Rules gain one optional `task:` match field. Backends gain optional `supports_tasks:` list. Everything else (health, priority, cost, audit, failover) reuses existing infrastructure.

---

## Why this is the right shape

The router's rule engine, health tracker, cost recorder, rate limiter, middleware stack, and audit log are all content-agnostic — they operate on metadata in headers, not on request bodies. The coupling to `/v1/chat/completions` is confined to ~3 dispatcher functions in `proxy.go` that parse `ChatRequest` and know how to talk to `vllm` / `ollama` backend types.

A task-aware proxy keeps that infrastructure and adds a parallel dispatch path that bypasses body parsing. Backends opt into tasks via a list; rules select a backend based on `X-Kronaxis-Task`. The body is forwarded byte-for-byte with the original `Content-Type` (so multipart/form-data, binary image bytes, raw audio, etc. all work). Response is forwarded byte-for-byte with its own `Content-Type` (streaming or buffered based on backend hint).

Alternatives considered and rejected:

- **Per-task endpoints** (`/v1/embeddings`, `/v1/images/generations`, etc.): ties the router to every task format. Copies chat completions' parsing pattern N times. High maintenance, low payoff.
- **Generic `/proxy/{backend}/{path}`**: simple but disables the rule engine. Callers have to pick the backend, losing routing, failover, and quality-based selection. Only good for ad-hoc debugging.

---

## Public API

### `POST /v1/task`

Request headers (required):
- `X-Kronaxis-Task: <task-id>` — selects which rule/backend serves the request. Examples: `yolo`, `embed`, `tts`, `whisper`, `diffusion`, `captcha-solve`.
- `Content-Type: <whatever the backend expects>` — JSON, multipart, octet-stream, etc. Router does not parse.
- `X-Kronaxis-Service: <caller>` — for cost/audit (existing).
- `X-Kronaxis-Priority: <interactive|normal|background|bulk>` — existing.

Request body: **any bytes**. Router does not inspect or rewrite.

Response: backend's response forwarded as-is. Status code, headers (minus hop-by-hop), and body passed through. Streaming preserved when backend returns chunked or SSE.

Error responses (router-generated, JSON):
- `400` — missing `X-Kronaxis-Task` header.
- `404` — no rule matches the task.
- `503` — all candidate backends unhealthy or over `max_concurrent`.
- `504` — backend timeout.

### `GET /api/tasks`

New admin endpoint (behind existing auth). Lists known tasks, which rules match them, which backends support each. Used by the UI and by callers wanting to discover what's available.

---

## Config schema additions

### Backends gain `supports_tasks:`

```yaml
backends:
  - name: yolo-gpu0
    url: "http://localhost:8890"
    type: generic                      # new backend type
    max_concurrent: 8
    health_endpoint: "/health"
    supports_tasks:                    # NEW
      - yolo-detect
      - yolo-classify
    forward_path: "/detect"            # NEW (optional) — appended to URL when proxying
    forward_method: "POST"             # NEW (optional) — defaults to method of incoming request
```

Unset `supports_tasks` means the backend is LLM-only (today's behaviour, rule engine ignores it for task routing).

### Rules gain `task:` match field

```yaml
rules:
  - name: yolo-detect-route
    priority: 180
    match:
      task: yolo-detect                # NEW — matches X-Kronaxis-Task header
    backends:
      - yolo-gpu0
      - yolo-gpu1

  - name: embeddings-route
    priority: 170
    match:
      task: embed
    backends:
      - bge-m3-gpu0
```

Match precedence (existing code in `router.go:186`): if rule has `task:`, request must have matching `X-Kronaxis-Task`. Combines with existing `model:`/`call_type:` fields using AND semantics.

### New backend `type: generic`

Today: `vllm`, `ollama`. Add `generic` — no request-body inspection, no model-field rewriting, no response-body parsing. Just HTTP byte-proxy with health checks.

Existing `vllm`/`ollama` types stay unchanged. A single backend can serve one type only (generic backends don't take chat completions; vllm backends don't take arbitrary tasks).

---

## Code changes (estimated ~400-500 LOC)

All paths relative to `kronaxis-router/`.

### New files

**`handle_task.go`** (~200 LOC) — mirrors `handleChatCompletions` but:
- Reads `X-Kronaxis-Task` header, returns 400 if missing.
- Does NOT parse body; buffers or streams raw bytes.
- Builds `RouteMetadata` from headers (reuse `extractHeaders` in `middleware.go`).
- Sets `meta.Task = taskHeader` (new field, see below).
- Calls new `rtr.RouteCandidatesByTask(meta)` which filters backends whose `SupportsTasks` includes the task, then runs existing rule priority ordering.
- Dispatches via new `forwardToGenericBackend` (see below).
- Records cost/audit/quality using existing calls (cost is by-byte instead of by-token for generic — see `costs.go` extension).

**`dispatch_generic.go`** (~120 LOC) — `forwardToGenericBackend(backend, path, method, body, headers, meta)`:
- Construct target URL: `backend.URL + backend.ForwardPath` (or incoming path if empty).
- Copy whitelist of request headers (`Content-Type`, `Accept`, `X-Request-ID`, etc.). Drop hop-by-hop and auth headers.
- `http.NewRequest` with raw body reader.
- Use `backend.Client.Do(req)` — reuse existing shared client for connection pooling.
- Copy response status, headers, body to `w`. Support streaming (use `io.Copy`, respect `Transfer-Encoding: chunked`).
- Record `meta.Service`, latency, backend name, bytes in/out, status to audit log.
- On error or `status >= 500`: mark backend unhealthy, return to `handle_task` for failover retry (same pattern as chat completions).

### Modified files

**`config.go`** — `BackendConfig` gains `SupportsTasks []string \`yaml:"supports_tasks"\``, `ForwardPath string \`yaml:"forward_path"\``, `ForwardMethod string \`yaml:"forward_method"\``. `RuleConfig.Match` gains `Task string \`yaml:"task"\``. Validation: if any backend has `type: generic`, it MUST have `supports_tasks` non-empty.

**`router.go`** — `RouteRequest` gains `Task string`. `ruleMatch` (line 186) gains: `if m.Task != "" && !strings.EqualFold(m.Task, req.Task) { return false }`. New function `RouteCandidatesByTask` = `RouteCandidates` + filter on `backend.Config.SupportsTasks contains req.Task`.

**`main.go`** — one new line: `mux.HandleFunc("/v1/task", handleTask)`. Plus `mux.HandleFunc("/api/tasks", handleTasksList)` for discovery.

**`middleware.go`** — `extractHeaders` reads `X-Kronaxis-Task` into `meta.Task`.

**`costs.go`** — add bytes-based cost recording for generic backends (existing token-based recording stays for LLM backends). Could be simple flat per-request cost until we want byte-accurate billing.

**`proxy.go`** — no changes. `/v1/chat/completions` path unaffected.

**`ratelimit.go`** — extend `rateLimitMiddleware` to apply to `/v1/task` as well (currently hardcoded to chat completions path).

### Tests

- **`handle_task_test.go`** — 400 on missing task header, 404 on no matching rule, 503 on all unhealthy, successful byte-passthrough with mock backend.
- **`router_test.go`** — rule matching with task field, AND semantics with model + call_type + task.
- **`config_test.go`** — validate `supports_tasks` required when `type: generic`.
- **`proxy_test.go`** — regression: chat completions flow still works when rules mix task-backends and chat-backends.

---

## Migration path for existing services

Once the feature lands, move services behind the router one at a time. Each migration is config-only (no service code changes):

1. **YOLO interactable** (:8890) → `task: yolo-detect`. Currently atlas calls `http://localhost:8890` directly via `YOLO_URL` env. Change env to `http://localhost:8050/v1/task` and add `X-Kronaxis-Task: yolo-detect` header. Eventually replicate to 2 GPUs for failover.

2. **Captcha solver** (:9100) → `task: captcha-solve`. Forms multipart upload with image. Generic proxy handles multipart as opaque bytes. Atlas's captcha code already calls via URL, one-line change.

3. **Voice-agent / Qwen3-TTS** (:8850) → `task: tts-synthesize`. Binary audio response. Router streams bytes. Caller receives WAV/OGG as before.

4. **bge-m3 embeddings** (not yet served) → `task: embed`. Standalone vLLM on spare GPU or CPU. Panel Studio / search / RAG callers go via router.

5. **Whisper ASR** (not yet served) → `task: asr-transcribe`. Same pattern.

6. **Future image-gen / SDXL** → `task: image-generate`. Same pattern.

Each migration gains: failover, health-based load balancing, per-service rate limits, cost tracking, unified audit log, single dashboard view.

---

## Non-goals (explicit)

- **Content transformation** — router does not convert formats between caller and backend. If backend wants multipart and caller sent JSON, that's caller's problem.
- **Authentication translation** — router doesn't speak OAuth or mint tokens for backends. Backends use pre-shared static keys if they need them (injected via backend-level config).
- **Schema validation** — generic task bodies are opaque to the router. If a caller sends bad YOLO input, the YOLO backend rejects it and router forwards the 400.
- **Response reshaping** — response bytes forwarded as-is. No JSON wrapping, no envelope.
- **Priority-based preemption** — still respects existing `max_concurrent` queueing per backend, but does not preempt in-flight requests. (Matches today's chat-completion behaviour.)

---

## Risks / open questions

- **Timeout budgets per task.** Chat completions has a 120s default. YOLO expects sub-second. TTS may take 30s. Add optional `timeout_ms:` per rule, with backend default as fallback.
- **Request size limits.** Image uploads are ~1-5 MB. Need to raise default request-body limit from whatever Go's default is, but cap per-task (config) to prevent abuse.
- **Streaming responses.** SSE from LLMs already works for chat completions. For task responses, need to decide: stream-through by default, or buffer? Start with buffered, add stream flag to rule later.
- **Cost model.** Per-request flat cost is coarse. Per-GPU-second would be better but needs backend cooperation (timing). Start with per-request, iterate.
- **Backend discovery.** How do callers find the task name? Answer: `/api/tasks` endpoint + UI dashboard. Not a blocker for implementation, add in same PR.

---

## Delivery plan

Single PR, incremental commits so it's reviewable:

1. Config schema additions (+ tests).
2. `RouteRequest.Task` field + rule matching (+ tests).
3. Generic backend type + `forwardToGenericBackend` (+ tests with mock backend).
4. `handle_task.go` endpoint handler (+ end-to-end integration test).
5. `/api/tasks` discovery endpoint.
6. Rate-limit middleware path extension.
7. Docs: update `architecture.md`, `configuration.md`, `api-reference.md`.
8. Config example: add YOLO backend to `config-dl580.yaml.example` as a reference (DO NOT enable without provisioning the backend).

After merge, migrate services one at a time with config-only changes. Each migration is reversible by flipping a single env var back to the direct URL.

Total effort: **~1-2 days for the PR, ~half a day per service migration**.

---

## When to build this

Build when the second non-LLM service needs centralised routing/failover. Today only YOLO has multiple candidates (could replicate) and it's behind a single-host CPU path. Once we have:

- Embeddings serving at scale (for RAG / search), OR
- TTS/ASR in the voice-agent critical path, OR
- A second YOLO replica for failover,

the ROI on this feature turns positive. Until then, direct service access is fine; the design just needs to be on file so we don't accidentally re-architect away from it.
