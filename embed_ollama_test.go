package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedderRoundTrip(t *testing.T) {
	// Fake Ollama: returns a fixed 4-dim embedding for /api/embeddings.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			http.Error(w, "not found", 404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float32{0.1, 0.2, 0.3, 0.4}})
	}))
	defer srv.Close()

	e, err := newEmbedder(context.Background(), GraphifyEmbedderConfig{Type: "ollama", URL: srv.URL, Model: "nomic-embed-text"})
	if err != nil {
		t.Fatalf("construct ollama embedder: %v", err)
	}
	if e.Dim() != 4 {
		t.Errorf("dim = %d, want 4 (probed)", e.Dim())
	}
	if e.Name() != "ollama:nomic-embed-text" {
		t.Errorf("name = %q", e.Name())
	}
	vecs, err := e.Embed(context.Background(), "query", []string{"a", "b"})
	if err != nil || len(vecs) != 2 || len(vecs[0]) != 4 {
		t.Fatalf("embed: %v vecs=%v", err, vecs)
	}
}

func TestOllamaEmbedderProbeFailsOnDeadServer(t *testing.T) {
	_, err := newEmbedder(context.Background(), GraphifyEmbedderConfig{Type: "ollama", URL: "http://127.0.0.1:1"})
	if err == nil {
		t.Error("expected a probe error against a dead Ollama endpoint")
	}
}
