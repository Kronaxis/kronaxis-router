package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
)

// Semantic / fuzzy prompt caching (ROADMAP #4). Deterministic SHA-256 caching
// misses identical intents phrased differently. This embeds the prompt and, on
// a near-duplicate (cosine >= threshold) hit, returns the cached response
// without calling the LLM. Reuses the graphify embedder + pgvector.
//
// Lossy by nature (a "close enough" prompt returns a prior answer), so it is
// applied ONLY to requests already deemed cacheable (deterministic / temp 0),
// and gated behind a high default threshold (0.96). Off by default.

var (
	semCache           *SemanticCache
	semCacheHits       atomic.Uint64
	semCacheStores     atomic.Uint64
	defaultSemCacheSim = 0.96
)

type SemanticCache struct {
	emb       Embedder
	threshold float64
}

func newSemanticCache(emb Embedder, threshold float64) *SemanticCache {
	if threshold <= 0 || threshold > 1 {
		threshold = defaultSemCacheSim
	}
	return &SemanticCache{emb: emb, threshold: threshold}
}

// ensureSchema creates the kr_semcache table + HNSW index if absent.
func (sc *SemanticCache) ensureSchema(ctx context.Context) error {
	if db == nil {
		return fmt.Errorf("semantic cache: no database")
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS kr_semcache (
    id          BIGSERIAL PRIMARY KEY,
    embedding   VECTOR(%d),
    response    BYTEA NOT NULL,
    status      INT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`, sc.emb.Dim()),
		`CREATE INDEX IF NOT EXISTS kr_semcache_embedding_hnsw ON kr_semcache USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("semantic cache schema: %w", err)
		}
	}
	return nil
}

// Lookup returns a cached response for a prompt semantically >= threshold to a
// stored one. ok=false on miss or any error (caller proceeds normally).
func (sc *SemanticCache) Lookup(ctx context.Context, prompt string) (body []byte, status int, ok bool) {
	if db == nil || prompt == "" {
		return nil, 0, false
	}
	vecs, err := sc.emb.Embed(ctx, "query", []string{prompt})
	if err != nil || len(vecs) != 1 {
		return nil, 0, false
	}
	q := vectorLiteral(vecs[0])
	var (
		resp []byte
		st   int
		sim  float64
	)
	row := db.QueryRowContext(ctx, `
        SELECT response, status, 1 - (embedding <=> $1::vector) AS sim
        FROM kr_semcache
        ORDER BY embedding <=> $1::vector
        LIMIT 1`, q)
	if err := row.Scan(&resp, &st, &sim); err != nil {
		if err != sql.ErrNoRows {
			// transient/db error: treat as miss
		}
		return nil, 0, false
	}
	if sim < sc.threshold {
		return nil, 0, false
	}
	semCacheHits.Add(1)
	return resp, st, true
}

// Store records a prompt→response pair. Best-effort; errors are logged only.
func (sc *SemanticCache) Store(ctx context.Context, prompt string, body []byte, status int) {
	if db == nil || prompt == "" || len(body) == 0 {
		return
	}
	vecs, err := sc.emb.Embed(ctx, "query", []string{prompt})
	if err != nil || len(vecs) != 1 {
		return
	}
	q := vectorLiteral(vecs[0])
	if _, err := db.ExecContext(ctx,
		`INSERT INTO kr_semcache (embedding, response, status) VALUES ($1::vector, $2, $3)`,
		q, body, status); err != nil {
		logger.Printf("semantic cache store failed: %v", err)
		return
	}
	semCacheStores.Add(1)
}
