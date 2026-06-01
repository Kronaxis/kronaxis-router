# prose-compressor

Learned prose-compression sidecar for `kronaxis-router`. Wraps **LLMLingua-2**
behind a tiny HTTP API. The router calls it on the aggressive compress path for
prose segments only (never code or JSON), and falls back to its lexical passes
on any error — so this service is strictly additive and safe to deploy/remove.

## Why this exists

Lexical compression (whitespace, dedup, markdown-strip) barely touches prose.
LLMLingua-2 scores every token with a fine-tuned classifier and drops the
low-information ones, typically keeping ~50% of prose tokens at minimal answer
loss. The bert-base model is ~700MB and runs in well under a second on GPU —
much cheaper than perplexity-based LLMLingua-1 (which needs a full causal LM).

## Deploy (the GPU host, GPU)

```bash
# on the the GPU host (your-host)
sudo mkdir -p /opt/kronaxis/prose-compressor && cd /opt/kronaxis/prose-compressor
# copy app.py, requirements.txt here
python3 -m venv .venv && . .venv/bin/activate
pip install torch --index-url https://download.pytorch.org/whl/cu121   # match CUDA
pip install -r requirements.txt
HF_HOME=/var/cache/huggingface PROSE_DEVICE=cuda CUDA_VISIBLE_DEVICES=2 python app.py
```

Or as a service:

```bash
sudo cp prose-compressor.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now prose-compressor
```

## Smoke test

```bash
curl -s localhost:8056/health
curl -s localhost:8056/compress -H 'Content-Type: application/json' \
  -d '{"text":"This is a fairly verbose paragraph that contains a lot of filler words and could be expressed far more concisely without losing the core meaning.","rate":0.5}'
```

## Wire into the router

In the router's `config.yaml` under `graphify`:

```yaml
graphify:
  enabled: true
  default: compress
  prose_compressor:
    enabled: true
    url: http://gpu-host:8056/compress
    rate: 0.5          # fraction of prose tokens to KEEP
    min_chars: 600     # skip prose shorter than this
    timeout_ms: 8000
```

The learned compressor runs only in the aggressive `compress` path (never the
always-on lossless pass), only on prose segments, and only when the segment is
at least `min_chars`. If the service is down or slow, the router logs nothing
fatal and uses its lexical result.

## Resource notes

- GPU memory: ~1–1.5 GB for the bert-base LLMLingua-2 model.
- Pin `CUDA_VISIBLE_DEVICES` so it doesn't contend with the vLLM tiers.
- First request after start pays the model-load cost; `/health` warms nothing,
  so consider a warm-up `/compress` call on boot if first-request latency matters.
