package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// retrieveRequest is the body for POST /v1/retrieve.
//
// MinCosineSim is *float64 so the JSON decoder can tell "field omitted →
// fall back to graphify config default" from "field set to 0.0 → no filter".
type retrieveRequest struct {
	Query          string   `json:"query"`
	TopK           int      `json:"top_k,omitempty"`
	MinCosineSim   *float64 `json:"min_cosine_sim,omitempty"`
	MaxChars       int      `json:"max_chars,omitempty"`
	BM25Weight     float64  `json:"bm25_weight,omitempty"`
	PathPrefixOnly string   `json:"path_prefix,omitempty"`
}

type retrieveResponse struct {
	Query    string            `json:"query"`
	Results  []RetrievalResult `json:"results"`
	Embedder string            `json:"embedder"`
	Took     string            `json:"took"`
}

func handleGraphifyRetrieve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if graphifyMW == nil || !graphifyMW.Enabled() || graphifyEmbedder == nil {
		http.Error(w, "graphify not enabled", http.StatusServiceUnavailable)
		return
	}
	defer r.Body.Close()
	var req retrieveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), 400)
		return
	}
	if req.Query == "" {
		http.Error(w, "query required", 400)
		return
	}
	// Resolve MinCosineSim: caller-supplied if set, else the gateway's
	// configured default (which itself falls back to 0.4 if not in config).
	minCos := graphifyMW.cfg.EffectiveMinCosineSim()
	if req.MinCosineSim != nil {
		minCos = *req.MinCosineSim
	}
	t0 := time.Now()
	results, err := graphifyRetrieve(r.Context(), db, graphifyEmbedder, RetrieveOpts{
		Query:          req.Query,
		TopK:           req.TopK,
		MinCosineSim:   minCos,
		MaxChars:       req.MaxChars,
		BM25Weight:     req.BM25Weight,
		PathPrefixOnly: req.PathPrefixOnly,
	})
	if err != nil {
		http.Error(w, "retrieve: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(retrieveResponse{
		Query:    req.Query,
		Results:  results,
		Embedder: graphifyEmbedder.Name(),
		Took:     time.Since(t0).String(),
	})
}

type graphifyStatsResponse struct {
	Enabled               bool   `json:"enabled"`
	Embedder              string `json:"embedder,omitempty"`
	EmbedderDim           int    `json:"embedder_dim,omitempty"`
	Mode                  string `json:"default_mode"`
	RequestsTotal         uint64 `json:"requests_total"`
	AugmentsTotal         uint64 `json:"augments_total"`
	CompressTotal         uint64 `json:"compress_total"`
	OffTotal              uint64 `json:"off_total"`
	ChunksRetrievedTotal  uint64 `json:"chunks_retrieved_total"`
	TokensSavedTotal      uint64 `json:"tokens_saved_total"`
	ErrorsTotal           uint64 `json:"errors_total"`
}

func handleGraphifyStats(w http.ResponseWriter, _ *http.Request) {
	resp := graphifyStatsResponse{
		Enabled:              graphifyMW != nil && graphifyMW.Enabled(),
		RequestsTotal:        graphifyRequestsTotal.Load(),
		AugmentsTotal:        graphifyAugmentsTotal.Load(),
		CompressTotal:        graphifyCompressTotal.Load(),
		OffTotal:             graphifyOffTotal.Load(),
		ChunksRetrievedTotal: graphifyChunksTotal.Load(),
		TokensSavedTotal:     graphifyTokensSavedTotal.Load(),
		ErrorsTotal:          graphifyErrorsTotal.Load(),
	}
	if graphifyEmbedder != nil {
		resp.Embedder = graphifyEmbedder.Name()
		resp.EmbedderDim = graphifyEmbedder.Dim()
	}
	if graphifyMW != nil {
		resp.Mode = graphifyMW.cfg.Default
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
