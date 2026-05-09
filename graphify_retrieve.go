package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// RetrievalResult is one chunk returned by Retrieve, scored by hybrid
// (cosine + BM25 reranking).
type RetrievalResult struct {
	ID           int64           `json:"id"`
	SourcePath   string          `json:"source_path"`
	ChunkIdx     int             `json:"chunk_idx"`
	Content      string          `json:"content"`
	Score        float64         `json:"score"`
	CosineSim    float64         `json:"cosine_sim"`
	BM25Score    float64         `json:"bm25_score"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	IngestedAtMS int64           `json:"ingested_at_ms,omitempty"`
}

// RetrieveOpts is a fully-resolved options struct: the caller is responsible
// for setting MinCosineSim to the desired value, including 0 (no filter) or a
// positive threshold. The HTTP handler and middleware resolve unset values
// from config defaults before calling.
type RetrieveOpts struct {
	Query          string
	TopK           int
	MinCosineSim   float64 // 0 = no filter; positive = drop chunks below
	MaxChars       int     // total budget across returned chunks (~ token budget * 4)
	BM25Weight     float64 // 0..1 reranking weight; 0 = pure cosine
	PathPrefixOnly string  // optional: filter by path prefix (e.g. only this project)
}

func graphifyRetrieve(ctx context.Context, db *sql.DB, emb Embedder, opts RetrieveOpts) ([]RetrievalResult, error) {
	if opts.TopK <= 0 {
		opts.TopK = 5
	}
	// MinCosineSim is now an honoured literal: 0 = "no filter", positive =
	// "drop chunks scoring below". Callers (config + HTTP) resolve unset to
	// the desired default before reaching here.
	if opts.MaxChars <= 0 {
		opts.MaxChars = 3200
	}
	if opts.BM25Weight < 0 || opts.BM25Weight > 1 {
		opts.BM25Weight = 0.3
	}

	vecs, err := emb.Embed(ctx, "query", []string{opts.Query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embed returned %d vectors, want 1", len(vecs))
	}
	q := vectorLiteral(vecs[0])

	// We pull TopK*4 candidates by cosine, then rerank with BM25.
	cosineLimit := opts.TopK * 4
	if cosineLimit < 16 {
		cosineLimit = 16
	}

	args := []interface{}{q, opts.Query, cosineLimit}
	pathClause := ""
	if opts.PathPrefixOnly != "" {
		pathClause = "AND source_path LIKE $4"
		args = append(args, opts.PathPrefixOnly+"%")
	}
	query := fmt.Sprintf(`
WITH cosine_top AS (
    SELECT id, source_path, chunk_idx, content, metadata,
           1 - (embedding <=> $1::vector) AS cosine_sim,
           (EXTRACT(EPOCH FROM ingested_at) * 1000)::BIGINT AS ingested_ms
    FROM kr_chunks
    WHERE embedding IS NOT NULL %s
    ORDER BY embedding <=> $1::vector
    LIMIT $3
)
SELECT id, source_path, chunk_idx, content, metadata, cosine_sim,
       COALESCE(ts_rank_cd(to_tsvector('english', content), websearch_to_tsquery('english', $2)), 0)::DOUBLE PRECISION AS bm25_score,
       ingested_ms
FROM cosine_top`, pathClause)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieve query: %w", err)
	}
	defer rows.Close()

	var results []RetrievalResult
	for rows.Next() {
		var r RetrievalResult
		var rawMD []byte
		if err := rows.Scan(&r.ID, &r.SourcePath, &r.ChunkIdx, &r.Content, &rawMD, &r.CosineSim, &r.BM25Score, &r.IngestedAtMS); err != nil {
			return nil, err
		}
		r.Metadata = rawMD
		results = append(results, r)
	}

	// Combine scores. Both are in different magnitudes; normalise BM25 to [0,1]
	// by dividing by max in batch.
	maxBM25 := 0.0
	for _, r := range results {
		if r.BM25Score > maxBM25 {
			maxBM25 = r.BM25Score
		}
	}
	for i := range results {
		bm := 0.0
		if maxBM25 > 0 {
			bm = results[i].BM25Score / maxBM25
		}
		results[i].Score = (1-opts.BM25Weight)*results[i].CosineSim + opts.BM25Weight*bm
	}
	// Sort desc by combined score
	sortDesc(results)

	// Drop low-similarity, then enforce char budget
	filtered := results[:0]
	used := 0
	for _, r := range results {
		if r.CosineSim < opts.MinCosineSim {
			continue
		}
		if used+len(r.Content) > opts.MaxChars && len(filtered) > 0 {
			break
		}
		filtered = append(filtered, r)
		used += len(r.Content)
		if len(filtered) >= opts.TopK {
			break
		}
	}
	return filtered, nil
}

func sortDesc(rs []RetrievalResult) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Score < rs[j].Score; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}
