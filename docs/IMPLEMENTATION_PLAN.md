# Implementation Plan

Technical specifications for each roadmap feature, in implementation order. Each section covers: what changes, where in the codebase, config schema additions, new API endpoints, test requirements, and dependencies on prior features.

Reference the current architecture in [architecture.md](architecture.md) and the public roadmap in [ROADMAP.md](../ROADMAP.md).

---

## Feature 1: KV Cache-Aware Routing (Radix Tree Pinning)

**Priority:** 1 (build first)
**Estimated scope:** ~400 lines, new file `kvpinning.go` + integration into `router.go`
**Dependencies:** None

### Problem

When a vLLM cluster has multiple nodes, round-robin routing forces each node to recompute the KV cache for multi-turn conversations. A 100k-token system prompt gets reprocessed on every turn if the request lands on a different node.

### Design

Maintain a radix trie in memory, keyed on the SHA-256 prefix of the first N messages in the prompt array (the "stable prefix"). Each leaf stores the backend name that last processed this prefix and a timestamp.

On routing:
1. Compute the prefix hash from the request's messages (all messages except the last user message).
2. Look up the prefix in the trie.
3. If a match exists and the backend is healthy and available, prefer it over the rule-ordered candidate list (insert it at position 0).
4. If the pinned backend is down or at capacity, fall through to normal routing.
5. After a successful response, update the trie with the backend that handled it.

Entries expire after a configurable TTL (default: 30 minutes, matching typical vLLM KV cache eviction).

### Files to change

| File | Change |
|------|--------|
| **`kvpinning.go` (new)** | `PrefixTrie` struct: `Insert(prefixHash, backendName)`, `Lookup(prefixHash) -> (backendName, found)`, `evictionLoop()`. Thread-safe with `sync.RWMutex`. |
| **`router.go`** | In `RouteCandidates()`, after rule evaluation produces candidates, call `prefixTrie.Lookup()`. If a pinned backend is in the candidate list, move it to index 0. If not in candidates but healthy, prepend it. |
| **`proxy.go`** | After successful response in `handleChatCompletions()`, call `prefixTrie.Insert(prefixHash, backend.Config.Name)`. |
| **`config.go`** | Add `KVPinning` field to `ServerConfig`. |
| **`main.go`** | Initialise `PrefixTrie`, start eviction loop. |

### Prefix hash computation

```go
func computePrefixHash(messages []ChatMessage) string {
    // Use all messages except the last (which is the new user turn)
    if len(messages) <= 1 {
        return ""
    }
    prefix := messages[:len(messages)-1]
    data, _ := json.Marshal(prefix)
    h := sha256.Sum256(data)
    return hex.EncodeToString(h[:16])
}
```

### Config schema addition

```yaml
server:
  kv_pinning:
    enabled: true              # Default: false
    ttl: 30m                   # How long to remember prefix-to-backend mapping
    max_entries: 10000          # Max trie entries before LRU eviction
```

### New metrics

```
kronaxis_router_kv_pin_hits_total      # Requests routed to a pinned backend
kronaxis_router_kv_pin_misses_total    # Prefix not found or pinned backend unavailable
kronaxis_router_kv_pin_entries         # Current trie size (gauge)
```

### Tests (`kvpinning_test.go`)

1. Insert and lookup: prefix found, correct backend returned.
2. TTL expiry: entry expires after TTL, lookup returns miss.
3. Eviction: entries evicted when max_entries exceeded (oldest first).
4. Pinned backend down: lookup returns miss, falls through to normal routing.
5. Single-message request: no prefix hash computed, no pinning.
6. Integration: two requests with same prefix routed to same backend.
7. Integration: pinned backend at max_concurrent, falls through.

### Notes

- The trie does not need to be a true radix trie for v1. A flat `map[string]pinnedEntry` keyed on the prefix hash is sufficient. The "radix trie" name describes the concept (prefix-based routing); the implementation can start simple and optimise later if needed.
- Only applies to backends with `type: vllm`. Cloud APIs manage their own caching.

---

## Feature 2: Stateful Session Management

**Priority:** 2 (build second)
**Estimated scope:** ~500 lines, new file `sessions.go` + integration into `proxy.go`
**Dependencies:** None (but compounds with KV Pinning: session context is always the same prefix, guaranteeing pin hits)

### Problem

Agentic workflows re-upload the entire conversation context with every HTTP request. A Claude Code session with a 100k-token system prompt sends those 100k tokens 50+ times.

### Design

**Session creation:**
1. Client sends a request with `X-Kronaxis-Session-Create: true` header.
2. Router processes the request normally.
3. Router stores the full messages array (minus the last user message) keyed by a generated session ID.
4. Response includes `X-Kronaxis-Session-ID: sess_<uuid>` header.

**Session hydration:**
1. Client sends a request with `X-Kronaxis-Session-ID: sess_<uuid>` header and only the new message(s).
2. Router looks up the stored context and prepends it to the request's messages array.
3. The hydrated request is forwarded to the backend as if the client had sent the full context.
4. Router updates the stored session with the new messages (appends user message + assistant response).

**Session lifecycle:**
- Sessions expire after a configurable TTL (default: 2 hours).
- `X-Kronaxis-Session-End: true` header explicitly destroys a session.
- Max sessions configurable (default: 1000), LRU eviction.

### Files to change

| File | Change |
|------|--------|
| **`sessions.go` (new)** | `SessionStore` struct: `Create(messages) -> sessionID`, `Hydrate(sessionID, newMessages) -> fullMessages`, `Update(sessionID, newMessages)`, `Destroy(sessionID)`, `evictionLoop()`. Thread-safe. |
| **`proxy.go`** | In `handleChatCompletions()`, before routing: check for session headers. If `Session-Create`, store after response. If `Session-ID`, hydrate before routing. If `Session-End`, destroy. |
| **`config.go`** | Add `Sessions` field to `ServerConfig`. |
| **`main.go`** | Initialise `SessionStore`, start eviction. |
| **`api.go`** | `GET /api/sessions` (list active), `DELETE /api/sessions?id=X` (destroy). |
| **`mcp.go`** | Add `router_sessions` and `router_create_session` tools. |

### Config schema addition

```yaml
server:
  sessions:
    enabled: true              # Default: false
    ttl: 2h                    # Session expiry
    max_sessions: 1000         # Max concurrent sessions, LRU eviction
    max_context_tokens: 500000 # Max tokens stored per session (safety limit)
```

### New headers

| Header | Direction | Purpose |
|--------|-----------|---------|
| `X-Kronaxis-Session-Create` | Request | `true` to create a new session from this request's context |
| `X-Kronaxis-Session-ID` | Request/Response | Session identifier for hydration |
| `X-Kronaxis-Session-End` | Request | `true` to destroy the session |
| `X-Kronaxis-Session-Tokens` | Response | Token count of stored session context |

### New API endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/sessions` | GET | List active sessions (ID, token count, created, last used) |
| `/api/sessions?id=X` | DELETE | Destroy a session |

### New metrics

```
kronaxis_router_sessions_active         # Current active sessions (gauge)
kronaxis_router_session_hydrations_total # Requests hydrated from session context
kronaxis_router_session_tokens_saved    # Tokens not re-transmitted (counter)
```

### Tests (`sessions_test.go`)

1. Create session: returns session ID in response header.
2. Hydrate: subsequent request with session ID gets full context.
3. Update: session context grows with each turn.
4. TTL expiry: session expires, hydration returns 404.
5. Explicit destroy: session deleted, hydration returns 404.
6. Max sessions: oldest session evicted when limit reached.
7. Invalid session ID: returns 400, not 500.
8. No session header: request processed normally (no regression).
9. Integration with KV pinning: session requests pin to same backend.

### Interaction with KV Pinning

When a session is hydrated, the prefix hash is computed from the session's stored context. This guarantees the same prefix hash on every turn, which means KV pinning always routes to the same backend. The combination is powerful: zero re-upload AND zero KV recomputation.

---

## Feature 3: Queue-Aware Load Balancing

**Priority:** 3
**Estimated scope:** ~200 lines, new file `queueaware.go` + integration into `backends.go` and `router.go`
**Dependencies:** None (but complements KV Pinning)

### Problem

Health checks tell you if a backend is alive, not if it's busy. When one vLLM node has 50 requests queued and another has 2, routing to the first adds unnecessary latency.

### Design

Periodically scrape the `/metrics` endpoint of each vLLM backend. Parse two Prometheus counters:
- `vllm:num_requests_waiting` (queued, not yet processing)
- `vllm:num_requests_running` (currently generating)

Store these as `QueueDepth` and `ActiveInference` on the `Backend` struct. In the routing path, when multiple backends are candidates, sort by queue depth (ascending).

### Files to change

| File | Change |
|------|--------|
| **`queueaware.go` (new)** | `QueueScraper`: periodic goroutine, HTTP GET to `/metrics`, parse Prometheus text format for the two counters. Update `Backend.QueueDepth` and `Backend.ActiveInference`. |
| **`backends.go`** | Add `QueueDepth int64` and `ActiveInference int64` fields to `Backend`. Add `QueueLoad() int64` method (returns `QueueDepth + ActiveInference`). Add these to `backendStatusInfo` JSON. |
| **`router.go`** | In `resolveAllBackends()`, after filtering healthy backends, sort by `QueueLoad()` ascending. Only sort when `queue_aware_routing` is enabled. |
| **`config.go`** | Add `QueueAwareRouting` and `QueueScrapeInterval` to `ServerConfig`. |
| **`main.go`** | Start `QueueScraper` goroutine if enabled. |

### Prometheus text parsing

Only need to extract two specific counter values. No need for a full Prometheus parser:

```go
func parseVLLMMetrics(body []byte) (waiting, running int64) {
    for _, line := range strings.Split(string(body), "\n") {
        if strings.HasPrefix(line, "vllm:num_requests_waiting ") {
            fmt.Sscanf(line, "vllm:num_requests_waiting %d", &waiting)
        }
        if strings.HasPrefix(line, "vllm:num_requests_running ") {
            fmt.Sscanf(line, "vllm:num_requests_running %d", &running)
        }
    }
    return
}
```

### Config schema addition

```yaml
server:
  queue_aware_routing: true        # Default: false
  queue_scrape_interval: 5s        # Default: 5s
```

### New metrics

```
kronaxis_router_backend_queue_depth{backend}     # Scraped queue depth per backend (gauge)
kronaxis_router_backend_active_inference{backend} # Active inference count per backend (gauge)
```

### Tests (`queueaware_test.go`)

1. Parse vLLM metrics: extract waiting and running counts.
2. Parse empty/malformed metrics: returns 0, 0 (no crash).
3. Sort by queue load: backends sorted correctly.
4. Queue-aware disabled: no sorting applied.
5. Non-vLLM backends: skipped by scraper (no /metrics probe for cloud APIs).
6. Integration: request routed to least-loaded backend.

### Interaction with KV Pinning

KV pinning suggests a backend based on cache warmth. Queue-aware LB suggests a backend based on load. When both are active, the logic is:

1. KV pinning finds a pinned backend.
2. If the pinned backend's queue load is below a threshold (e.g., 2x the average), use it (warm cache is worth some extra wait).
3. If the pinned backend is heavily loaded, fall through to queue-aware ordering.

This threshold is configurable: `kv_pinning.max_queue_ratio: 2.0` (default).

---

## Feature 4: Schema-Validated Quality Gates

**Priority:** 4
**Estimated scope:** ~200 lines added to `qualitygate.go`
**Dependencies:** None

### Problem

The existing quality gate validates response quality by checking length, JSON validity, and refusal patterns. It does not validate against a user-supplied schema. A cheap model that returns `{"name": "foo"}` when the schema requires `{"name": string, "score": number}` passes the current checks but breaks the caller.

### Design

Add a new quality check: `SchemaValidation`. The user supplies a JSON Schema via:
- `X-Kronaxis-Response-Schema` header (inline JSON Schema), or
- `response_format.json_schema` field in the OpenAI request body (native OpenAI structured output)

If the response fails schema validation, the quality gate retries on the fallback backend.

### Files to change

| File | Change |
|------|--------|
| **`qualitygate.go`** | Add `SchemaJSON string` to `GateChecks`. In `passesChecks()`, add schema validation step: unmarshal response content, validate against schema. Use Go's `encoding/json` for basic type/required-field validation (no external JSON Schema library needed for v1). |
| **`proxy.go`** | Extract `X-Kronaxis-Response-Schema` header and `response_format.json_schema` from request body. Pass schema string to quality gate. |
| **`config.go`** | No config changes needed (schema is per-request, not per-rule). |

### Schema validation (v1: lightweight, no external dependency)

For v1, validate:
- Response is valid JSON
- All `required` fields are present
- Field types match (`string`, `number`, `boolean`, `array`, `object`)
- Nested objects validated recursively

This covers 95% of use cases without adding a JSON Schema library dependency. Full JSON Schema Draft 2020-12 compliance can be added later if needed.

```go
func validateAgainstSchema(content string, schemaJSON string) bool {
    var schema map[string]interface{}
    if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
        return true // Invalid schema = skip validation
    }
    var data interface{}
    if err := json.Unmarshal([]byte(content), &data); err != nil {
        return false // Response is not valid JSON
    }
    return validateNode(data, schema)
}
```

### New header

| Header | Direction | Purpose |
|--------|-----------|---------|
| `X-Kronaxis-Response-Schema` | Request | JSON Schema for response validation |

### New metrics

```
kronaxis_router_schema_validations_total     # Requests validated against schema
kronaxis_router_schema_failures_total        # Schema validation failures (retried)
```

### Tests (add to `qualitygate_test.go`)

1. Valid response against schema: passes.
2. Missing required field: fails, retries on fallback.
3. Wrong type (string where number expected): fails.
4. Nested object validation: works recursively.
5. No schema provided: quality gate works as before (no regression).
6. Invalid schema JSON: skipped gracefully.
7. Array response validated correctly.
8. Markdown-fenced JSON response: fences stripped before validation.

---

## Feature 5: DPO Dataset Export

**Priority:** 5
**Estimated scope:** ~80 lines, new file `dpo.go` + integration into `qualitygate.go`
**Dependencies:** Feature 4 (Schema Quality Gate) for schema-based failures, but also works with existing quality gate failures

### Problem

Every quality gate fallback (cheap model fails, expensive model succeeds) is a free training pair for Direct Preference Optimization. Currently these are logged in the audit log but not in a training-ready format.

### Design

When a quality gate fallback fires:
1. Log the prompt, the rejected response (cheap model), and the chosen response (expensive model) as a JSONL line.
2. Append to a configurable output file.
3. Rotate after N entries.

### Files to change

| File | Change |
|------|--------|
| **`dpo.go` (new)** | `DPOLogger` struct: `LogPair(prompt, rejected, chosen)`. Writes JSONL. File rotation. Thread-safe. |
| **`qualitygate.go`** | After a fallback fires in both `GateSequential` and `GateParallel`, call `dpoLogger.LogPair()` with the prompt, cheap response, and expensive response. |
| **`config.go`** | Add `DPO` field to a new `QualityGateConfig` sub-struct or top-level. |
| **`main.go`** | Initialise `DPOLogger` if enabled. |

### Output format (JSONL)

```json
{"prompt": [{"role": "user", "content": "..."}], "chosen": "expensive model output", "rejected": "cheap model output", "timestamp": "2026-04-14T10:30:00Z", "cheap_backend": "local-small", "expensive_backend": "cloud-fast"}
```

### Config schema addition

```yaml
quality_gate:
  dpo_export:
    enabled: true
    output_path: "dpo_training_data.jsonl"   # Default
    max_file_size_mb: 100                     # Rotate after 100MB
```

### Tests (`dpo_test.go`)

1. Pair logged on quality gate fallback.
2. JSONL format is valid (each line parses independently).
3. No logging when quality gate passes (cheap model succeeded).
4. File rotation at max size.
5. Disabled: no file created.

---

## Feature 6: Circuit Breaking

**Priority:** 6
**Estimated scope:** ~60 lines added to `backends.go`
**Dependencies:** None

### Problem

The current health check interval is 30 seconds. If a provider throws five 503s in 2 seconds, traffic keeps flowing to it until the next probe.

### Design

Add a sliding window error tracker per backend. When N errors occur within M seconds, immediately set status to `StatusDown`. Recovery: on the next scheduled health check, if the probe succeeds, restore to `StatusHealthy`.

### Files to change

| File | Change |
|------|--------|
| **`backends.go`** | Add `errorTimestamps []time.Time` to `Backend`. Add `RecordError()` method: appends timestamp, checks if last N timestamps fall within M seconds, trips circuit if so. Add `CircuitBreaker` config fields to `BackendConfig`. Modify `checkOne()` to clear error timestamps on successful probe. |
| **`proxy.go`** | After a failed `forwardWithRetry()`, call `backend.RecordError()` (currently only increments `Failures`). |
| **`config.go`** | Add `CircuitBreaker` struct to `BackendConfig`. |

### Config schema addition

```yaml
backends:
  - name: cloud-fast
    circuit_breaker:
      error_threshold: 5       # Default: 5
      window_seconds: 10       # Default: 10
```

### Tests (add to `backends_test.go`)

1. Circuit trips after N errors in M seconds.
2. Circuit does not trip for N-1 errors.
3. Circuit does not trip for N errors spread over >M seconds.
4. Circuit recovers on next successful health check.
5. No circuit breaker config: existing behaviour unchanged.

---

## Feature 7: Shadow Routing

**Priority:** 7
**Estimated scope:** ~40 lines added to `abtest.go` and `proxy.go`
**Dependencies:** None

### Problem

How do you prove a cheaper model delivers comparable quality before committing to the switch?

### Design

Add a `mode` field to `ABTestConfig`: `"split"` (existing behaviour) or `"shadow"`. In shadow mode:
- 100% of traffic goes to `variant_a` (returned to caller).
- `split_pct`% is also sent to `variant_b` (fire-and-forget, response logged but not returned).
- Compare outputs using the existing quality validator similarity metric.

### Files to change

| File | Change |
|------|--------|
| **`abtest.go`** | Add `Mode string` field (`split` or `shadow`). In `SelectVariant()`, when mode is `shadow`, always return variant_a as the primary, but set a flag indicating shadow dispatch is needed. Add `ShadowResults` stats (similarity scores, cost delta). |
| **`proxy.go`** | After response from primary, if shadow flag is set, fire-and-forget goroutine sends the same request to variant_b. Log the comparison. |

### Config schema addition

```yaml
ab_tests:
  - name: gemini-migration
    match: { service: "my-api" }
    variant_a: cloud-expensive
    variant_b: cloud-cheap
    split_pct: 10
    mode: shadow                # "split" (default) or "shadow"
```

### New API endpoint

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/ab-tests` | GET | List A/B tests with stats (including shadow comparison metrics) |

### Tests (add to `abtest_test.go`)

1. Shadow mode: caller always gets variant_a response.
2. Shadow dispatch: variant_b receives traffic (verify via stats).
3. Split mode: unchanged behaviour (no regression).
4. Shadow comparison logged: similarity score recorded.

---

## Feature 8: Cost Forecasting

**Priority:** 8
**Estimated scope:** ~50 lines added to `costs.go` and `api.go`
**Dependencies:** None

### Problem

Budget enforcement catches you at the limit. By then it's too late to adjust workloads.

### Design

Linear extrapolation from current spend:
- At time T, service S has spent $X since midnight.
- Hours elapsed = T - midnight.
- Projected daily spend = $X * (24 / hours_elapsed).
- Estimated budget exhaustion time = midnight + (daily_limit / hourly_rate) hours.

### Files to change

| File | Change |
|------|--------|
| **`costs.go`** | Add `Forecast(service) -> (projectedDailySpend, exhaustionTime)` method. |
| **`api.go`** | Add `GET /api/costs/forecast` endpoint: returns per-service forecast. |
| **`mcp.go`** | Add `router_cost_forecast` tool. |

### New API endpoint

| Endpoint | Method | Response |
|----------|--------|----------|
| `/api/costs/forecast` | GET | `[{"service": "my-api", "spent_today": 12.50, "projected_daily": 45.00, "budget": 50.00, "exhaustion_time": "2026-04-14T14:15:00Z"}]` |

### Tests (add to `costs_test.go`)

1. Forecast at 25% of day with 25% of budget spent: projects hitting limit at EOD.
2. Forecast at 10% of day with 50% of budget: projects exhaustion before noon.
3. No spend yet: returns null exhaustion time.
4. No budget set: returns projected spend without exhaustion time.

---

## Feature 9: Predictive SLA Routing (Reactive)

**Priority:** 9
**Estimated scope:** ~150 lines, new file `sla.go` + integration into `router.go`
**Dependencies:** None (benefits from queue-aware data)

### Problem

Static rules route to backends that may be experiencing latency spikes. A rule saying "use cloud-fast" doesn't help when cloud-fast has a P95 of 3 seconds.

### Design

Track a rolling window (last 100 requests) of TTFT per backend. Expose `P50()` and `P95()` methods. In the routing path, when a rule has a `max_ttft_ms` constraint, filter out backends whose P95 exceeds it.

### Files to change

| File | Change |
|------|--------|
| **`sla.go` (new)** | `LatencyTracker` struct per backend: circular buffer of TTFT values, `Record(ttft)`, `P50()`, `P95()` methods. |
| **`proxy.go`** | After first token received (or full response for non-streaming), record TTFT via `latencyTracker.Record()`. |
| **`router.go`** | In `resolveAllBackends()`, if rule has `max_ttft_ms > 0`, filter backends where P95 exceeds the limit. |
| **`config.go`** | Add `MaxTTFTms` to `RoutingRule`. |

### Config schema addition

```yaml
rules:
  - name: latency-sensitive
    match: { priority_level: interactive }
    backends: [local-fast, cloud-fast]
    max_ttft_ms: 800            # Filter backends with P95 TTFT above this
```

### New metrics

```
kronaxis_router_backend_ttft_p50_ms{backend}   # Rolling P50 TTFT (gauge)
kronaxis_router_backend_ttft_p95_ms{backend}   # Rolling P95 TTFT (gauge)
```

### Tests (`sla_test.go`)

1. P50/P95 computed correctly from known values.
2. Circular buffer wraps: old values replaced.
3. Backend filtered when P95 exceeds max_ttft_ms.
4. No max_ttft_ms: no filtering applied.
5. All backends filtered: falls through to default chain (not empty result).

---

## Feature 10: MCP Server Expansion

**Priority:** 10
**Estimated scope:** ~100 lines added to `mcp.go`
**Dependencies:** Features 2, 8 (session management, cost forecasting)

### New MCP tools

| Tool | Purpose |
|------|---------|
| `router_sessions` | List active sessions (ID, token count, age) |
| `router_create_session` | Create a session from provided messages |
| `router_cost_forecast` | Per-service cost forecast and budget exhaustion estimate |
| `router_shadow_results` | Shadow routing comparison results |
| `router_backend_queue` | Queue depth and active inference per backend |

These are thin wrappers around the API endpoints added by the preceding features.

---

## Implementation sequence

```
Week 1:  Feature 1 (KV Pinning) + Feature 6 (Circuit Breaking)
Week 2:  Feature 2 (Session Management) + Feature 5 (DPO Export)
Week 3:  Feature 3 (Queue-Aware LB) + Feature 4 (Schema Quality Gate)
Week 4:  Feature 7 (Shadow Routing) + Feature 8 (Cost Forecasting)
Week 5:  Feature 9 (SLA Routing) + Feature 10 (MCP Expansion)
```

Features within each week are independent and can be built in parallel.

## New files summary

| File | Feature | Lines (est.) |
|------|---------|-------------|
| `kvpinning.go` | KV Cache Pinning | ~200 |
| `kvpinning_test.go` | Tests | ~150 |
| `sessions.go` | Session Management | ~300 |
| `sessions_test.go` | Tests | ~200 |
| `queueaware.go` | Queue-Aware LB | ~120 |
| `queueaware_test.go` | Tests | ~80 |
| `dpo.go` | DPO Export | ~60 |
| `dpo_test.go` | Tests | ~40 |
| `sla.go` | SLA Routing | ~100 |
| `sla_test.go` | Tests | ~60 |

Estimated total: ~1,310 new lines + ~200 lines of modifications to existing files.

## Config schema (complete view after all features)

```yaml
server:
  port: 8050
  health_check_interval: 30s
  default_timeout: 120s
  # New: KV pinning
  kv_pinning:
    enabled: false
    ttl: 30m
    max_entries: 10000
    max_queue_ratio: 2.0       # Max queue load ratio before ignoring pin
  # New: Queue-aware routing
  queue_aware_routing: false
  queue_scrape_interval: 5s
  # New: Session management
  sessions:
    enabled: false
    ttl: 2h
    max_sessions: 1000
    max_context_tokens: 500000

backends:
  - name: my-backend
    url: "http://localhost:8000"
    type: vllm
    # ... existing fields ...
    # New: Circuit breaker
    circuit_breaker:
      error_threshold: 5
      window_seconds: 10

rules:
  - name: my-rule
    # ... existing fields ...
    # New: SLA constraint
    max_ttft_ms: 0             # 0 = no constraint

# New: Quality gate DPO export
quality_gate:
  # ... existing fields ...
  dpo_export:
    enabled: false
    output_path: "dpo_training_data.jsonl"
    max_file_size_mb: 100

# New: Shadow mode for A/B tests
ab_tests:
  - name: test
    mode: split                # "split" or "shadow"
    # ... existing fields ...
```

## New API endpoints summary

| Endpoint | Method | Feature |
|----------|--------|---------|
| `/api/sessions` | GET | Session Management |
| `/api/sessions?id=X` | DELETE | Session Management |
| `/api/costs/forecast` | GET | Cost Forecasting |
| `/api/ab-tests` | GET | Shadow Routing |

## New response headers summary

| Header | Feature |
|--------|---------|
| `X-Kronaxis-Session-ID` | Session Management |
| `X-Kronaxis-Session-Tokens` | Session Management |
| `X-Kronaxis-KV-Pin: HIT` | KV Pinning |

## New environment variables

| Variable | Default | Feature |
|----------|---------|---------|
| `SESSION_MAX_SIZE` | `1000` | Session Management |
| `SESSION_TTL_SECONDS` | `7200` | Session Management |
| `DPO_OUTPUT_PATH` | `dpo_training_data.jsonl` | DPO Export |
