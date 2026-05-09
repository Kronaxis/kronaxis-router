package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Embedder is a pluggable embedding backend. Implementations: local
// sentence-transformers sidecar (default), Gemini text-embedding-004, OpenAI
// text-embedding-3-small. Configure via `graphify.embedder.type` in config.
type Embedder interface {
	Name() string
	Dim() int
	// Embed returns one vector per text. Role is "passage" (default) or
	// "query" -- some models prepend an instruction prefix per role.
	Embed(ctx context.Context, role string, texts []string) ([][]float32, error)
}

// newEmbedder reads cfg.Graphify.Embedder and returns the configured backend.
// Probes the backend (via /healthz or a tiny embed call) to learn its dim.
func newEmbedder(ctx context.Context, ec GraphifyEmbedderConfig) (Embedder, error) {
	switch strings.ToLower(ec.Type) {
	case "", "local-st", "sentence-transformers":
		url := ec.URL
		if url == "" {
			url = "http://localhost:8053"
		}
		e := &localSTEmbedder{url: url, client: &http.Client{Timeout: 60 * time.Second}}
		if err := e.probe(ctx); err != nil {
			return nil, fmt.Errorf("probe local-st embedder %s: %w", url, err)
		}
		return e, nil
	case "gemini":
		key := os.Getenv(ec.APIKeyEnv)
		if ec.APIKeyEnv == "" {
			key = os.Getenv("GEMINI_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("gemini embedder: missing api key (env %s)", ec.APIKeyEnv)
		}
		model := ec.Model
		if model == "" {
			model = "text-embedding-004"
		}
		baseURL := ec.URL
		if baseURL == "" {
			baseURL = "https://generativelanguage.googleapis.com/v1beta"
		}
		// Gemini text-embedding-004 returns 768-dim vectors by default.
		dim := ec.Dim
		if dim == 0 {
			dim = 768
		}
		return &geminiEmbedder{
			apiKey:  key,
			baseURL: baseURL,
			model:   model,
			dim:     dim,
			client:  &http.Client{Timeout: 60 * time.Second},
		}, nil
	case "openai":
		key := os.Getenv(ec.APIKeyEnv)
		if ec.APIKeyEnv == "" {
			key = os.Getenv("OPENAI_API_KEY")
		}
		if key == "" {
			return nil, fmt.Errorf("openai embedder: missing api key")
		}
		model := ec.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		baseURL := ec.URL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		dim := ec.Dim
		if dim == 0 {
			dim = 1536
		}
		return &openAIEmbedder{
			apiKey:  key,
			baseURL: baseURL,
			model:   model,
			dim:     dim,
			client:  &http.Client{Timeout: 60 * time.Second},
		}, nil
	default:
		return nil, fmt.Errorf("unknown embedder type %q", ec.Type)
	}
}

// ── local sentence-transformers sidecar ───────────────────────────────

type localSTEmbedder struct {
	url    string
	client *http.Client
	dim    int
	model  string
}

func (e *localSTEmbedder) Name() string { return "local-st:" + e.model }
func (e *localSTEmbedder) Dim() int     { return e.dim }

func (e *localSTEmbedder) probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: %d", resp.StatusCode)
	}
	var hr struct {
		Model string `json:"model"`
		Dim   int    `json:"dim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		return err
	}
	e.dim = hr.Dim
	e.model = hr.Model
	return nil
}

func (e *localSTEmbedder) Embed(ctx context.Context, role string, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"texts": texts, "role": role})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed %d: %s", resp.StatusCode, string(buf))
	}
	var er struct {
		Embeddings [][]float32 `json:"embeddings"`
		Dim        int         `json:"dim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	return er.Embeddings, nil
}

// ── Gemini text-embedding-004 ─────────────────────────────────────────

type geminiEmbedder struct {
	apiKey  string
	baseURL string
	model   string
	dim     int
	client  *http.Client
}

func (e *geminiEmbedder) Name() string { return "gemini:" + e.model }
func (e *geminiEmbedder) Dim() int     { return e.dim }

func (e *geminiEmbedder) Embed(ctx context.Context, role string, texts []string) ([][]float32, error) {
	taskType := "RETRIEVAL_DOCUMENT"
	if role == "query" {
		taskType = "RETRIEVAL_QUERY"
	}
	type content struct {
		Parts []map[string]string `json:"parts"`
	}
	type embReq struct {
		Model    string  `json:"model"`
		Content  content `json:"content"`
		TaskType string  `json:"taskType"`
	}
	type batchReq struct {
		Requests []embReq `json:"requests"`
	}

	requests := make([]embReq, 0, len(texts))
	for _, t := range texts {
		requests = append(requests, embReq{
			Model:    "models/" + e.model,
			Content:  content{Parts: []map[string]string{{"text": t}}},
			TaskType: taskType,
		})
	}
	body, _ := json.Marshal(batchReq{Requests: requests})

	url := strings.TrimRight(e.baseURL, "/") + "/models/" + e.model + ":batchEmbedContents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini embed %d: %s", resp.StatusCode, string(buf))
	}
	var er struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	out := make([][]float32, len(er.Embeddings))
	for i, e := range er.Embeddings {
		out[i] = e.Values
	}
	return out, nil
}

// ── OpenAI text-embedding-3-small ─────────────────────────────────────

type openAIEmbedder struct {
	apiKey  string
	baseURL string
	model   string
	dim     int
	client  *http.Client
}

func (e *openAIEmbedder) Name() string { return "openai:" + e.model }
func (e *openAIEmbedder) Dim() int     { return e.dim }

func (e *openAIEmbedder) Embed(ctx context.Context, _ string, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{
		"input": texts,
		"model": e.model,
	})
	url := strings.TrimRight(e.baseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai embed %d: %s", resp.StatusCode, string(buf))
	}
	var er struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	out := make([][]float32, len(er.Data))
	for i, d := range er.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
