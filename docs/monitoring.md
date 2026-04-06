# Monitoring Guide

## Prometheus Metrics

Scrape `/metrics` with Prometheus:

```yaml
scrape_configs:
  - job_name: kronaxis-router
    static_configs:
      - targets: ['kronaxis-router:8050']
    scrape_interval: 30s
```

### Available Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kronaxis_router_requests_total` | counter | service, backend, rule | Total requests processed |
| `kronaxis_router_errors_total` | counter | service, backend, rule | Requests with 4xx/5xx status |
| `kronaxis_router_request_duration_ms_bucket` | histogram | service, backend, rule | Latency distribution (10/25/50/100/250/500/1000/5000ms) |
| `kronaxis_router_cache_hits_total` | counter | | Cache hits |
| `kronaxis_router_cache_misses_total` | counter | | Cache misses |
| `kronaxis_router_batch_submitted_total` | counter | | Batch jobs submitted |
| `kronaxis_router_batch_completed_total` | counter | | Batch jobs completed |
| `kronaxis_router_batch_failed_total` | counter | | Batch jobs failed |
| `kronaxis_router_backend_healthy` | gauge | backend, type | 1=healthy, 0=degraded/down |
| `kronaxis_router_backend_active_requests` | gauge | backend, type | In-flight request count |
| `kronaxis_router_uptime_seconds` | gauge | | Process uptime |

### Grafana Dashboard Queries

**Request rate by backend:**
```promql
rate(kronaxis_router_requests_total[5m])
```

**Error rate:**
```promql
rate(kronaxis_router_errors_total[5m]) / rate(kronaxis_router_requests_total[5m]) * 100
```

**P99 latency:**
```promql
histogram_quantile(0.99, rate(kronaxis_router_request_duration_ms_bucket[5m]))
```

**Cache hit rate:**
```promql
rate(kronaxis_router_cache_hits_total[5m]) / (rate(kronaxis_router_cache_hits_total[5m]) + rate(kronaxis_router_cache_misses_total[5m])) * 100
```

**Backend health:**
```promql
kronaxis_router_backend_healthy
```

### Alerts

```yaml
groups:
  - name: kronaxis-router
    rules:
      - alert: AllBackendsDown
        expr: sum(kronaxis_router_backend_healthy) == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "All Kronaxis Router backends are down"

      - alert: HighErrorRate
        expr: rate(kronaxis_router_errors_total[5m]) / rate(kronaxis_router_requests_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Error rate above 10%"

      - alert: HighLatency
        expr: histogram_quantile(0.95, rate(kronaxis_router_request_duration_ms_bucket[5m])) > 5000
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "P95 latency above 5 seconds"
```

## Health Endpoint

`GET /health` returns backend status, cache stats, and quality validation state. Use for load balancer health checks (returns 200 when operational).

## Audit Log

When `AUDIT_ENABLED=true`, all requests are logged to `AUDIT_LOG_FILE` (default `audit.jsonl`) in JSONL format with PII auto-redacted:

```json
{"timestamp":"2026-04-06T12:00:00Z","service":"my-api","call_type":"summarise","backend":"local-large","status_code":200,"latency_ms":150,"input_tokens":100,"output_tokens":50,"cost":0.0001,"prompt":"[REDACTED] asked about...","response":"The answer is..."}
```

Redacted patterns: emails, phone numbers (US/UK), SSNs, credit cards, postcodes, API keys, JWT tokens. Custom patterns can be added via config.

Log rotates after `AUDIT_MAX_ENTRIES` (default 100,000) entries.

## Cost Dashboard

`GET /api/costs?period=today|week|month` returns a full cost breakdown by service, model, and call type. Requires `DATABASE_URL` to be set for persistent tracking.

The web UI at `/` provides a visual cost dashboard with bar charts and daily/budget comparisons.
