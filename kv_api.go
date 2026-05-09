package main

import (
	"encoding/json"
	"net/http"
)

// handleKVTrees returns a snapshot of every per-backend prefix tree's
// node count + max-age, useful for monitoring how warm each backend's
// inferred KV cache is. Called from main.go's mux registration.
func handleKVTrees(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if kvIndex == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object":  "list",
			"enabled": false,
			"data":    []any{},
		})
		return
	}
	stats := kvIndex.Stats()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object":            "list",
		"enabled":           true,
		"backend_count":     len(stats),
		"hash_chunk_tokens": kvIndex.hashChunkTokens,
		"trees":             stats,
	})
}
