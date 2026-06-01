package main

import (
	"encoding/json"
	"net/http"
)

// handleCCRRetrieve serves the original content for a CCR id produced during
// compression. GET /v1/compress/retrieve?id=<id> — returns the raw content as
// text/plain, or JSON metadata with ?format=json.
func handleCCRRetrieve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if ccrStore == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "CCR store is not enabled")
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErrorJSON(w, http.StatusBadRequest, "missing id parameter")
		return
	}
	content, ok := ccrStore.Get(id)
	if !ok {
		writeErrorJSON(w, http.StatusNotFound, "no stored content for id "+id)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      id,
			"bytes":   len(content),
			"content": content,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(content))
}
