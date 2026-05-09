# embedding-service

Sentence-Transformers sidecar used by kronaxis-router's graphify pre-stage. Stateless HTTP. One model loaded at startup; reload by restarting the container with a different `EMBEDDING_MODEL`.

## Endpoints

- `POST /embed` — body `{"texts": ["..."], "role": "passage|query"}` → `{"embeddings": [[...]], "model": "...", "dim": N, "count": N}`
- `GET /healthz` — `{"status":"ok","model":"...","dim":N}`

## Configuration

| Env var | Default | Notes |
|---|---|---|
| `EMBEDDING_MODEL` | `BAAI/bge-small-en-v1.5` | Any Sentence-Transformers model. 384 dim default; switching dim requires wiping the `kr_chunks` table. |
| `EMBEDDING_PORT` | `8053` | |
| `EMBEDDING_QUERY_PREFIX` | `""` | Some models recommend prefixes (e.g. `bge` wants `Represent this sentence for searching relevant passages: ` for queries). |
| `EMBEDDING_PASSAGE_PREFIX` | `""` | Most models leave this empty. |

## Recommended models

| Model | Dim | Size | Notes |
|---|---|---|---|
| `BAAI/bge-small-en-v1.5` | 384 | 130 MB | Default. Beats MiniLM at the same dim. |
| `sentence-transformers/all-MiniLM-L6-v2` | 384 | 80 MB | Smallest, fastest, baseline quality. |
| `BAAI/bge-base-en-v1.5` | 768 | 440 MB | Better retrieval, 2× index size. |
| `nomic-ai/nomic-embed-text-v1.5` | 768 | 550 MB | Long-context (8k); needs `trust_remote_code`. |
| `intfloat/multilingual-e5-base` | 768 | 1.1 GB | Multilingual support if you ever need it. |

## Local run (without Docker)

```bash
cd kronaxis-router/embedding-service
pip install -r requirements.txt
python server.py
```

## Docker

Built and run as part of the kronaxis-router stack via `docker-compose`. Standalone:

```bash
cd kronaxis-router/embedding-service
docker build -t kronaxis/embedding-service .
docker run --rm -p 8053:8053 kronaxis/embedding-service
```

CPU-only by default. For GPU, swap base image to `pytorch/pytorch:2.3.1-cuda12.1-runtime` and add `--gpus all` to docker run. Not worth the complexity for a 130 MB model — CPU inference is ~20 ms/sentence on a modern x86, fine for the router.
