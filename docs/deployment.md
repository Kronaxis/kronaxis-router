# Deployment Guide

## Docker (Standalone)

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o kronaxis-router .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/kronaxis-router /app/kronaxis-router
COPY config.yaml /app/config.yaml
EXPOSE 8050
CMD ["/app/kronaxis-router"]
```

```bash
docker build -t kronaxis-router .
docker run -d \
  -p 8050:8050 \
  -v /path/to/config.yaml:/app/config.yaml \
  -e GEMINI_API_KEY=xxx \
  -e ROUTER_API_TOKEN=xxx \
  -e DATABASE_URL=postgres://... \
  kronaxis-router
```

## Docker Compose

```yaml
services:
  kronaxis-router:
    build: ./kronaxis-router
    container_name: kronaxis-router
    ports:
      - "8050:8050"
    volumes:
      - ./config.yaml:/app/config.yaml
      - router-batches:/app/batches
    environment:
      - DATABASE_URL=postgres://user:pass@db:5432/mydb?sslmode=disable
      - GEMINI_API_KEY=${GEMINI_API_KEY}
      - ROUTER_API_TOKEN=${ROUTER_API_TOKEN}
      - ROUTER_PORT=8050
      - BATCH_DATA_DIR=/app/batches
      - CACHE_MAX_SIZE=2000
      - ROUTER_ALLOW_PRIVATE_BACKENDS=true
    depends_on:
      - db
    restart: unless-stopped

volumes:
  router-batches:
```

## Kubernetes (Helm)

### Install

```bash
# From the repo
helm install kronaxis-router ./helm/kronaxis-router \
  --namespace llm-infra \
  --create-namespace \
  --set env.GEMINI_API_KEY=$GEMINI_API_KEY \
  --set env.ROUTER_API_TOKEN=$ROUTER_API_TOKEN

# With custom config
kubectl create configmap kronaxis-router-config \
  --from-file=config.yaml=./my-config.yaml

helm install kronaxis-router ./helm/kronaxis-router \
  --set config.existingConfigMap=kronaxis-router-config
```

### With Secrets

```bash
kubectl create secret generic kronaxis-router-secrets \
  --from-literal=GEMINI_API_KEY=$GEMINI_API_KEY \
  --from-literal=ROUTER_API_TOKEN=$ROUTER_API_TOKEN \
  --from-literal=DATABASE_URL=postgres://...

helm install kronaxis-router ./helm/kronaxis-router \
  --set existingSecret=kronaxis-router-secrets
```

### With Ingress

```yaml
# values-production.yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: router.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: router-tls
      hosts:
        - router.example.com
```

### With Prometheus

```yaml
serviceMonitor:
  enabled: true
  interval: 30s
  path: /metrics
```

## Production Checklist

- [ ] Set `ROUTER_API_TOKEN` (protects management APIs)
- [ ] Set `DATABASE_URL` (enables persistent cost tracking)
- [ ] Review `config.yaml` backend URLs and API keys
- [ ] Set `CACHE_MAX_SIZE` based on expected traffic patterns
- [ ] Configure budgets for each service
- [ ] Configure rate limits for each service
- [ ] Set up Prometheus scraping on `/metrics`
- [ ] Set up alerts on `kronaxis_router_backend_healthy` gauge
- [ ] Consider `AUDIT_ENABLED=true` for compliance
- [ ] Restrict network access (router should not be internet-facing without auth)
- [ ] If deploying on cloud: ensure `ROUTER_ALLOW_PRIVATE_BACKENDS` is NOT set (SSRF protection)

## Reverse Proxy (Nginx)

```nginx
upstream kronaxis-router {
    server 127.0.0.1:8050;
}

server {
    listen 443 ssl;
    server_name router.example.com;

    ssl_certificate /etc/letsencrypt/live/router.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/router.example.com/privkey.pem;

    location / {
        proxy_pass http://kronaxis-router;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;                    # Required for SSE streaming
        proxy_cache off;
        proxy_read_timeout 120s;
    }
}
```

## Resource Sizing

| Traffic Level | CPU | Memory | Notes |
|--------------|-----|--------|-------|
| Low (<10 req/s) | 100m | 128Mi | Default Helm values |
| Medium (10-100 req/s) | 500m | 256Mi | |
| High (100-1000 req/s) | 2 cores | 1Gi | Consider multiple replicas |
| Very high (>1000 req/s) | 4 cores | 2Gi | Multiple replicas + shared DB |

The router is stateless except for in-memory caches and batch jobs. Multiple replicas can run behind a load balancer, but each maintains its own cache and batch job state. Use `DATABASE_URL` for shared cost tracking across replicas.
