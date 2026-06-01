"""Learned prose-compression service for kronaxis-router.

Wraps LLMLingua-2 (a lightweight token-classification model, NOT a 7B causal LM)
behind a tiny HTTP API. The router calls POST /compress on the aggressive
compress path for prose segments; on any failure the router falls back to its
lexical passes, so this service is strictly additive.

Why LLMLingua-2: it keeps the high-information tokens and drops filler by scoring
each token with a fine-tuned BERT/XLM-RoBERTa classifier. The bert-base model is
~700MB and runs in well under a second per request on a dedicated GPU — far cheaper
than perplexity-based LLMLingua-1 which needs a full causal LM.

Env:
  PROSE_MODEL   HF model id (default microsoft/llmlingua-2-bert-base-multilingual-cased-meetingbank)
  PROSE_DEVICE  cuda | cpu (default: cuda if available)
  PROSE_PORT    listen port (default 8056)
"""
import os

import torch
import uvicorn
from fastapi import FastAPI
from llmlingua import PromptCompressor
from pydantic import BaseModel

MODEL = os.getenv(
    "PROSE_MODEL",
    "microsoft/llmlingua-2-bert-base-multilingual-cased-meetingbank",
)
DEVICE = os.getenv("PROSE_DEVICE", "cuda" if torch.cuda.is_available() else "cpu")
PORT = int(os.getenv("PROSE_PORT", "8056"))

app = FastAPI(title="kronaxis prose-compressor")
compressor = PromptCompressor(model_name=MODEL, use_llmlingua2=True, device_map=DEVICE)

# Punctuation / newlines we never want dropped — keeps the output readable and
# preserves structure the model downstream relies on.
FORCE_TOKENS = ["\n", ".", ",", "?", "!", ":", ";"]


class CompressReq(BaseModel):
    text: str
    rate: float = 0.5  # target fraction of tokens to KEEP


class CompressResp(BaseModel):
    compressed: str
    origin_tokens: int
    compressed_tokens: int
    ratio: float


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL, "device": DEVICE, "use_llmlingua2": True}


@app.post("/compress", response_model=CompressResp)
def compress(req: CompressReq):
    rate = req.rate if 0.0 < req.rate < 1.0 else 0.5
    result = compressor.compress_prompt(
        req.text,
        rate=rate,
        force_tokens=FORCE_TOKENS,
        drop_consecutive=True,
    )
    return CompressResp(
        compressed=result["compressed_prompt"],
        origin_tokens=int(result.get("origin_tokens", 0)),
        compressed_tokens=int(result.get("compressed_tokens", 0)),
        ratio=float(result.get("rate", rate)) if isinstance(result.get("rate"), (int, float)) else rate,
    )


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=PORT)
