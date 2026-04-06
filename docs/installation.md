# Installation Guide

## Prerequisites

- Go 1.22+ (for building from source)
- Docker (optional, for containerised deployment)
- Kubernetes + Helm (optional, for Helm chart deployment)

## Option 1: Download Binary

Download the latest release for your platform:

```bash
# Linux (amd64)
curl -sL https://github.com/kronaxis/kronaxis-router/releases/latest/download/kronaxis-router-linux-amd64.tar.gz | tar xz
sudo mv kronaxis-router /usr/local/bin/

# macOS (arm64)
curl -sL https://github.com/kronaxis/kronaxis-router/releases/latest/download/kronaxis-router-darwin-arm64.tar.gz | tar xz
sudo mv kronaxis-router /usr/local/bin/

# Verify
kronaxis-router --version
```

## Option 2: Build from Source

```bash
git clone https://github.com/kronaxis/kronaxis-router.git
cd kronaxis-router
go build -o kronaxis-router .
./kronaxis-router
```

## Option 3: Docker

```bash
docker build -t kronaxis-router .
docker run -p 8050:8050 -v $(pwd)/config.yaml:/app/config.yaml kronaxis-router
```

Or with Docker Compose:

```yaml
services:
  kronaxis-router:
    build: ./kronaxis-router
    ports:
      - "8050:8050"
    volumes:
      - ./config.yaml:/app/config.yaml
    environment:
      - GEMINI_API_KEY=${GEMINI_API_KEY}
      - DATABASE_URL=postgres://user:pass@db:5432/mydb?sslmode=disable
```

## Option 4: Helm (Kubernetes)

```bash
helm install kronaxis-router ./helm/kronaxis-router \
  --set env.GEMINI_API_KEY=$GEMINI_API_KEY \
  --set env.ROUTER_API_TOKEN=$ROUTER_API_TOKEN
```

See [Deployment Guide](deployment.md) for detailed Kubernetes setup.

## Option 5: Python SDK

```bash
pip install kronaxis-router
```

```python
from kronaxis_router import KronaxisRouter
router = KronaxisRouter("http://localhost:8050", service="my-app")
response = router.chat("Hello, world!")
```

## Option 6: TypeScript SDK

```bash
npm install kronaxis-router
```

```typescript
import { KronaxisRouter } from 'kronaxis-router';
const router = new KronaxisRouter('http://localhost:8050', { service: 'my-app' });
const response = await router.chat('Hello, world!');
```

## First Run

On first run without a config file, the router auto-generates a default `config.yaml`:

```bash
./kronaxis-router
# [router] no config file at config.yaml, generating default
# [router] loaded config: 1 backends, 1 rules
# [router] listening on :8050
```

Open http://localhost:8050 to access the web UI.

## Verify Installation

```bash
curl http://localhost:8050/health
```

Expected response:
```json
{
  "status": "ok",
  "service": "kronaxis-router",
  "version": "1.0.0",
  "backends_total": 1,
  "backends_healthy": 0
}
```

## Next Steps

1. [Configure backends and routing rules](configuration.md)
2. [Point your services at the router](user-guide.md)
3. [Set up monitoring](monitoring.md)
