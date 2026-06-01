# Learned prose-compressor — design

**Status:** Code + service shipped 2026-06-01. GPU service not yet deployed (needs a GPU host + model download). Router-side client is merged and degrades to lexical when the endpoint is absent.

## Problem

The content-aware compressor handles JSON (compaction + tabularisation) and code (comment + whitespace stripping) well, but the **prose** path is purely lexical (whitespace, markdown-noise, line dedup). Real-tokenizer measurement: prose payloads compress only ~5–32%, versus 35–65% for JSON/code. Prose is the weakest path and often the bulk of agent context (instructions, retrieved docs, summaries).

## Approach

Add an optional **learned** compressor for prose, modelled on LLMLingua-2: a self-hosted GPU endpoint the router calls on the aggressive compress path. LLMLingua-2 uses a fine-tuned token-classification model (BERT/XLM-RoBERTa, ~700MB) to score each token and drop low-information ones — far cheaper than perplexity-based LLMLingua-1 (full causal LM).

This is **lossy**, so it is constrained to:
- the aggressive `compress` path only — never the always-on lossless pass;
- **prose segments only** — never code or JSON (those have their own lossless/near-lossless transforms);
- segments ≥ `min_chars` (default 600);
- best-effort — any error/timeout/non-shrinking result keeps the lexical body.

## Components

| Piece | Location | Role |
|---|---|---|
| `PromptCompressor` interface + HTTP client | `prose_compressor.go` | calls the endpoint; owns its timeout; counts ok/fail |
| Content-router hook | `compress.go` `maybeLearnedProse` | runs after lexical passes on prose segments |
| Config | `config.go` `ProseCompressorConfig` | `graphify.prose_compressor.{enabled,url,rate,min_chars,timeout_ms}` |
| Wiring | `graphify_middleware.go` | builds the client; sets opts in compress() stage-1 |
| GPU service | `services/prose-compressor/` | FastAPI + LLMLingua-2; `POST /compress`, `GET /health` |

## Contract

```
POST /compress  {"text": "...", "rate": 0.5}
            ->  {"compressed": "...", "origin_tokens": N, "compressed_tokens": M, "ratio": R}
```
`rate` = fraction of prose tokens to KEEP (0–1; default 0.5).

## Safety / failure modes

- **Service down/slow** → router uses lexical result (timeout default 8s). No request ever fails because the compressor is unavailable.
- **Lossy** → only on `compress` mode prose; the always-on lossless pass is untouched. Operators who want zero prose loss simply leave `prose_compressor.enabled: false`.
- **GPU contention** → pin `CUDA_VISIBLE_DEVICES` to a dedicated GPU.

## Open items (the honest ceiling)

- Not yet deployed/measured on real GPU — the % win over lexical prose is expected but unverified end-to-end.
- No request-level rate adaptation (could scale `rate` by priority/budget).
- CCR store still in-memory (separate concern); if learned compression elides via CCR, durability matters more.
