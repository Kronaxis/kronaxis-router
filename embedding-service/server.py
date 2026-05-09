"""Embedding sidecar for kronaxis-router's graphify pre-stage.

Loads a Sentence-Transformers model once at startup and exposes:
  POST /embed       {"texts": ["..."]}        -> {"embeddings": [[...]], "model": "...", "dim": N}
  GET  /healthz                                -> {"status": "ok", "model": "...", "dim": N}

Model is configurable via EMBEDDING_MODEL env var. Default is bge-small-en-v1.5
(384 dim) -- better retrieval than MiniLM at the same dim, ~130 MB on disk.
"""
import os
from flask import Flask, request, jsonify
from sentence_transformers import SentenceTransformer

MODEL_ID = os.environ.get("EMBEDDING_MODEL", "BAAI/bge-small-en-v1.5")
PORT = int(os.environ.get("EMBEDDING_PORT", "8053"))
QUERY_PREFIX = os.environ.get("EMBEDDING_QUERY_PREFIX", "")
PASSAGE_PREFIX = os.environ.get("EMBEDDING_PASSAGE_PREFIX", "")

print(f"loading model {MODEL_ID}", flush=True)
model = SentenceTransformer(MODEL_ID)
DIM = model.get_sentence_embedding_dimension()
print(f"loaded {MODEL_ID} dim={DIM}", flush=True)

app = Flask(__name__)


@app.get("/healthz")
def healthz():
    return jsonify({"status": "ok", "model": MODEL_ID, "dim": DIM})


@app.post("/embed")
def embed():
    body = request.get_json(silent=True) or {}
    texts = body.get("texts", [])
    if not isinstance(texts, list) or not texts:
        return jsonify({"error": "texts must be a non-empty list"}), 400
    role = body.get("role", "passage")
    prefix = QUERY_PREFIX if role == "query" else PASSAGE_PREFIX
    if prefix:
        texts = [prefix + t for t in texts]
    vectors = model.encode(
        texts,
        normalize_embeddings=True,
        convert_to_numpy=True,
        show_progress_bar=False,
    )
    return jsonify({
        "embeddings": vectors.tolist(),
        "model": MODEL_ID,
        "dim": DIM,
        "count": len(texts),
    })


if __name__ == "__main__":
    print(f"listening on :{PORT}", flush=True)
    app.run(host="0.0.0.0", port=PORT, threaded=True)
