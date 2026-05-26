package main

// Kronaxis Platform integration: when graphify.fabric_url is set we
// delegate the RAG pre-stage to the Fabric service instead of running
// embedded pgvector + the local sentence-transformers embedder.
//
// This file is the only thing the router needs in order to participate
// in the platform; everything else (config loading, middleware splice,
// metrics) lives where graphify already lived.
//
// Wire model:
//
//   Router proxy  ──>  graphifyMW.Preprocess
//                          │
//                          ├──> fabricRetrieve(...)        [if fabric_url set]
//                          │       POST <fabric_url>/v1/rag
//                          │       Authorization: Bearer <fabric_key>
//                          │
//                          └──> graphifyRetrieve(...)      [embedded fallback]
//
// On any Fabric error we log + fall through to the embedded path so a
// flaky Fabric host never fails a chat request.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// FabricRAGRequest mirrors the wire shape of POST /v1/rag.
// Keep field names exactly as Fabric expects.
type FabricRAGRequest struct {
	Q              string           `json:"q"`
	TopK           int              `json:"top_k"`
	Weights        FabricRAGWeights `json:"weights"`
	Filters        FabricRAGFilters `json:"filters,omitempty"`
	MaxChunkTokens int              `json:"max_chunk_tokens,omitempty"`
}

type FabricRAGFilters struct {
	SourcePaths []string `json:"source_paths,omitempty"`
	Type        string   `json:"type,omitempty"`
	MinScore    float64  `json:"min_score,omitempty"`
}

// FabricChunk is one chunk returned by Fabric /v1/rag.
type FabricChunk struct {
	ID       int64           `json:"id"`
	Content  string          `json:"content"`
	Source   string          `json:"source"`
	Score    float64         `json:"score"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// FabricRAGResponse mirrors the wire shape of the /v1/rag response.
type FabricRAGResponse struct {
	Chunks              []FabricChunk `json:"chunks"`
	TotalTokensEstimate int           `json:"total_tokens_estimate"`
	RankingExplanation  string        `json:"ranking_explanation"`
}

// Metrics for the fabric-delegate path. Surfaced via the standard
// /metrics endpoint alongside the embedded-graphify counters.
var (
	graphifyFabricCallsTotal     atomic.Uint64
	graphifyFabricFailsTotal     atomic.Uint64
	graphifyFabricChunksTotal    atomic.Uint64
	graphifyFabricFallbacksTotal atomic.Uint64
)

// fabricRetrieve calls Fabric /v1/rag and adapts its response into the
// shape graphify already understands (RetrievalResult slice).
//
// Returns a non-nil error only on hard failures (HTTP non-2xx, JSON
// decode error, transport error). Empty results are reported as a
// nil slice with nil error -- that's a valid steady-state response
// when the Fabric chunk store hasn't been populated yet.
func fabricRetrieve(ctx context.Context, cfg GraphifyConfig, opts RetrieveOpts) ([]RetrievalResult, error) {
	graphifyFabricCallsTotal.Add(1)
	if !cfg.FabricEnabled() {
		// Should never happen if the caller checked; defend anyway.
		return nil, fmt.Errorf("fabric_url not configured")
	}

	body := FabricRAGRequest{
		Q:       opts.Query,
		TopK:    opts.TopK,
		Weights: cfg.EffectiveFabricWeights(),
		Filters: FabricRAGFilters{
			MinScore: opts.MinCosineSim,
		},
		MaxChunkTokens: opts.MaxChars / 4, // chars -> rough tokens
	}
	if opts.PathPrefixOnly != "" {
		body.Filters.SourcePaths = []string{opts.PathPrefixOnly + "%"}
	}
	if body.TopK <= 0 {
		body.TopK = 5
	}
	if body.MaxChunkTokens <= 0 {
		body.MaxChunkTokens = 400
	}
	buf, _ := json.Marshal(body)

	timeout := time.Duration(cfg.FabricTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := cfg.FabricURL + "/v1/rag"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		graphifyFabricFailsTotal.Add(1)
		return nil, fmt.Errorf("fabric: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.FabricKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.FabricKey)
	}
	req.Header.Set("User-Agent", "kronaxis-router/"+version+" (fabric-delegate)")

	logger.Printf("graphify: delegating to fabric url=%s top_k=%d weights=cosine:%.2f,tsvector:%.2f,recency:%.2f q_chars=%d",
		cfg.FabricURL, body.TopK, body.Weights.Cosine, body.Weights.TSVector, body.Weights.Recency, len(opts.Query))

	httpClient := &http.Client{Timeout: timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		graphifyFabricFailsTotal.Add(1)
		return nil, fmt.Errorf("fabric: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		graphifyFabricFailsTotal.Add(1)
		return nil, fmt.Errorf("fabric: HTTP %d", resp.StatusCode)
	}

	var fr FabricRAGResponse
	if err := json.NewDecoder(resp.Body).Decode(&fr); err != nil {
		graphifyFabricFailsTotal.Add(1)
		return nil, fmt.Errorf("fabric: decode: %w", err)
	}
	graphifyFabricChunksTotal.Add(uint64(len(fr.Chunks)))
	logger.Printf("graphify: fabric returned %d chunks (~%d tokens) ranking=%q",
		len(fr.Chunks), fr.TotalTokensEstimate, fr.RankingExplanation)

	// Adapt to the embedded RetrievalResult shape so the middleware
	// rendering code (renderContext) can stay generic.
	results := make([]RetrievalResult, 0, len(fr.Chunks))
	for _, c := range fr.Chunks {
		// Source is "path[:range]"; split on the last ':' that looks
		// like a range to populate SourcePath cleanly.
		sourcePath := c.Source
		if idx := strings.LastIndex(c.Source, ":"); idx > 0 {
			tail := c.Source[idx+1:]
			if isLikelyRange(tail) {
				sourcePath = c.Source[:idx]
			}
		}
		results = append(results, RetrievalResult{
			ID:         c.ID,
			SourcePath: sourcePath,
			Content:    c.Content,
			Score:      c.Score,
			CosineSim:  c.Score, // fabric's score is already the blended rank
			Metadata:   c.Metadata,
		})
	}
	return results, nil
}

// isLikelyRange returns true for tails that look like "42-89" or "42",
// used only to decide whether the final ':NN' on a chunk source is a
// line range vs part of the path (e.g. URLs).
func isLikelyRange(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '-' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// fabricMetricsLines returns Prometheus-formatted lines for the
// fabric-delegate counters. Called from the same /metrics handler that
// already emits the embedded-graphify counters.
func fabricMetricsLines() string {
	var b strings.Builder
	b.WriteString("# HELP kronaxis_router_graphify_fabric_calls_total Calls to Fabric /v1/rag\n")
	b.WriteString("# TYPE kronaxis_router_graphify_fabric_calls_total counter\n")
	fmt.Fprintf(&b, "kronaxis_router_graphify_fabric_calls_total %d\n", graphifyFabricCallsTotal.Load())

	b.WriteString("# HELP kronaxis_router_graphify_fabric_fails_total Fabric /v1/rag call failures\n")
	b.WriteString("# TYPE kronaxis_router_graphify_fabric_fails_total counter\n")
	fmt.Fprintf(&b, "kronaxis_router_graphify_fabric_fails_total %d\n", graphifyFabricFailsTotal.Load())

	b.WriteString("# HELP kronaxis_router_graphify_fabric_chunks_total Chunks returned by Fabric\n")
	b.WriteString("# TYPE kronaxis_router_graphify_fabric_chunks_total counter\n")
	fmt.Fprintf(&b, "kronaxis_router_graphify_fabric_chunks_total %d\n", graphifyFabricChunksTotal.Load())

	b.WriteString("# HELP kronaxis_router_graphify_fabric_fallbacks_total Fallbacks to embedded graphify after fabric failure\n")
	b.WriteString("# TYPE kronaxis_router_graphify_fabric_fallbacks_total counter\n")
	fmt.Fprintf(&b, "kronaxis_router_graphify_fabric_fallbacks_total %d\n", graphifyFabricFallbacksTotal.Load())
	return b.String()
}
