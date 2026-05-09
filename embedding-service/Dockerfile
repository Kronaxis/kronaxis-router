FROM python:3.11-slim AS base

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1

RUN apt-get update && apt-get install -y --no-install-recommends \
        curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# ── Layer: deps. Cached unless requirements.txt changes. ───────────────
COPY requirements.txt ./
RUN pip install -r requirements.txt

# ── Layer: model bake. Cached unless EMBEDDING_MODEL changes. ──────────
# Models are downloaded into /models-baked at build time. At runtime we
# copy them into /models (which may be a volume) on first start so the
# volume gets a populated cache without forcing a re-download.
ARG EMBEDDING_MODEL=BAAI/bge-small-en-v1.5
ENV EMBEDDING_MODEL=$EMBEDDING_MODEL \
    SENTENCE_TRANSFORMERS_HOME=/models-baked \
    HF_HOME=/models-baked \
    HF_HUB_OFFLINE=0
RUN python -c "from sentence_transformers import SentenceTransformer; \
SentenceTransformer('$EMBEDDING_MODEL'); \
print('baked', '$EMBEDDING_MODEL')"

# ── Layer: app code. Cached unless server.py / entrypoint.sh changes. ──
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
COPY server.py ./

# At runtime, switch the model path back to /models so the volume picks
# up the baked model (entrypoint copies if /models is empty). User can
# still pull a different model at runtime; it persists in the volume.
ENV SENTENCE_TRANSFORMERS_HOME=/models \
    HF_HOME=/models

EXPOSE 8053
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://localhost:8053/healthz || exit 1

ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["python", "-u", "server.py"]
