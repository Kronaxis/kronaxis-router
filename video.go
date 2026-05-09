package main

// video.go — Video generation routing for Kronaxis Router.
//
// Adds /v1/video/generate and /v1/video/* endpoints that route to
// backends with capability "video" (type: "ltx" or "video").
//
// The request format mirrors the LTX-Video server API:
//   POST /v1/video/generate
//   {
//     "prompt": "a gas engineer inspecting a boiler...",
//     "image": "base64...",         // optional: image-to-video
//     "duration_seconds": 5.0,
//     "width": 768, "height": 512,
//     "fps": 24,
//     "num_inference_steps": 20,
//     "guidance_scale": 3.0,
//     "seed": 42
//   }
//
// The router selects the first healthy video backend and proxies the
// request. If no video backend is available it returns 503.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// VideoGenerateRequest is the inbound request shape.
type VideoGenerateRequest struct {
	Prompt             string  `json:"prompt"`
	NegativePrompt     string  `json:"negative_prompt,omitempty"`
	Image              string  `json:"image,omitempty"`
	DurationSeconds    float64 `json:"duration_seconds,omitempty"`
	Width              int     `json:"width,omitempty"`
	Height             int     `json:"height,omitempty"`
	FPS                int     `json:"fps,omitempty"`
	NumInferenceSteps  int     `json:"num_inference_steps,omitempty"`
	GuidanceScale      float64 `json:"guidance_scale,omitempty"`
	Seed               *int64  `json:"seed,omitempty"`
	OutputFormat       string  `json:"output_format,omitempty"`
	// Kronaxis metadata (not forwarded to backend)
	Model    string `json:"model,omitempty"`    // if set, prefer specific backend
	Vertical string `json:"vertical,omitempty"` // persona vertical — for logging
}

// handleVideoGenerate selects a video backend and proxies the generation request.
func handleVideoGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Parse to get optional vertical/model hints for logging
	var req VideoGenerateRequest
	json.Unmarshal(body, &req)

	// Find a healthy video backend
	backend := selectVideoBackend(req.Model)
	if backend == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "no video backend available",
			"message": "LTX-Video service is not healthy. Check that ltx-server is running on port 8900.",
		})
		return
	}

	// Proxy to backend — strip Kronaxis-only fields before forwarding
	stripped, _ := json.Marshal(map[string]interface{}{
		"prompt":               req.Prompt,
		"negative_prompt":      req.NegativePrompt,
		"image":                req.Image,
		"duration_seconds":     req.DurationSeconds,
		"width":                req.Width,
		"height":               req.Height,
		"fps":                  req.FPS,
		"num_inference_steps":  req.NumInferenceSteps,
		"guidance_scale":       req.GuidanceScale,
		"seed":                 req.Seed,
		"output_format":        req.OutputFormat,
	})

	targetURL := strings.TrimRight(backend.URL, "/") + "/v1/video/generate"
	start := time.Now()

	proxyReq, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(stripped))
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	if backend.APIKey != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+backend.APIKey)
	}

	client := &http.Client{Timeout: 600 * time.Second} // video gen can take 5+ min
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[video] backend %s error: %v", backend.Name, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	log.Printf("[video] %s vertical=%s status=%d %.1fs",
		backend.Name, req.Vertical, resp.StatusCode, elapsed.Seconds())

	// Forward response
	for k, v := range resp.Header {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.Header().Set("X-Kronaxis-Backend", backend.Name)
	w.Header().Set("X-Kronaxis-Duration", elapsed.String())
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleVideoProxy proxies /v1/video/{id} retrieval requests to the video backend.
func handleVideoProxy(w http.ResponseWriter, r *http.Request) {
	backend := selectVideoBackend("")
	if backend == nil {
		http.Error(w, "no video backend available", http.StatusServiceUnavailable)
		return
	}

	targetURL := strings.TrimRight(backend.URL, "/") + r.URL.Path
	proxyReq, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// selectVideoBackend returns the first healthy backend with "video" capability.
// If preferName is set, tries to find that backend first.
func selectVideoBackend(preferName string) *BackendConfig {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	var fallback *BackendConfig

	for name, b := range pool.backends {
		if !hasCapabilitySlice(b.Config.Capabilities, "video") {
			continue
		}
		if b.Status == StatusDown {
			continue
		}
		if preferName != "" && name == preferName {
			cfg := b.Config
			return &cfg
		}
		if fallback == nil {
			cfg := b.Config
			fallback = &cfg
		}
	}
	return fallback
}

func hasCapabilitySlice(caps []string, target string) bool {
	for _, c := range caps {
		if c == target {
			return true
		}
	}
	return false
}
