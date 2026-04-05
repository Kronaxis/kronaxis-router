package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BatchJob represents an async batch job submitted to a provider's
// batch API for 50% cost reduction.
type BatchJob struct {
	ID           string       `json:"id"`
	Provider     string       `json:"provider"`    // openai, anthropic, gemini, mistral, groq, together, fireworks
	BackendName  string       `json:"backend_name"`
	Status       string       `json:"status"`      // pending, submitted, processing, completed, failed, expired
	InputFile    string       `json:"input_file"`
	OutputFile   string       `json:"output_file"`
	ProviderID   string       `json:"provider_id"` // provider's batch job ID
	RequestCount int          `json:"request_count"`
	CreatedAt    time.Time    `json:"created_at"`
	CompletedAt  *time.Time   `json:"completed_at,omitempty"`
	Error        string       `json:"error,omitempty"`
	CallbackURL  string       `json:"callback_url,omitempty"`
}

// BatchRequest is a single request within a batch job.
type BatchRequest struct {
	CustomID string       `json:"custom_id"`
	Method   string       `json:"method"`
	URL      string       `json:"url"`
	Body     ChatRequest  `json:"body"`
}

// BatchResult is a single result from a completed batch job.
type BatchResult struct {
	CustomID string `json:"custom_id"`
	Response struct {
		StatusCode int          `json:"status_code"`
		Body       ChatResponse `json:"body"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// BatchManager handles async batch job lifecycle.
type BatchManager struct {
	jobs    map[string]*BatchJob
	dataDir string
	mu      sync.RWMutex
}

func newBatchManager(dataDir string) *BatchManager {
	if dataDir == "" {
		dataDir = "/tmp/kronaxis-router-batches"
	}
	os.MkdirAll(dataDir, 0755)
	bm := &BatchManager{
		jobs:    make(map[string]*BatchJob),
		dataDir: dataDir,
	}
	bm.loadJobs()
	return bm
}

// loadJobs restores batch jobs from disk on startup.
func (bm *BatchManager) loadJobs() {
	path := filepath.Join(bm.dataDir, "jobs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return // No saved jobs
	}
	var jobs []*BatchJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		logger.Printf("failed to load batch jobs: %v", err)
		return
	}
	for _, j := range jobs {
		bm.jobs[j.ID] = j
		// Resume polling for submitted/processing jobs
		if j.Status == "submitted" || j.Status == "processing" {
			backend := pool.Get(j.BackendName)
			if backend != nil {
				go bm.pollJob(j, backend)
			}
		}
	}
	logger.Printf("restored %d batch jobs from disk", len(jobs))
}

// saveJobs persists all batch jobs to disk.
func (bm *BatchManager) saveJobs() {
	bm.mu.RLock()
	jobs := make([]*BatchJob, 0, len(bm.jobs))
	for _, j := range bm.jobs {
		jobs = append(jobs, j)
	}
	bm.mu.RUnlock()

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		logger.Printf("failed to marshal batch jobs: %v", err)
		return
	}
	path := filepath.Join(bm.dataDir, "jobs.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		logger.Printf("failed to save batch jobs: %v", err)
	}
}

// SubmitBatch creates an async batch job for 50% cost savings.
// Accepts an array of ChatRequest objects, writes them as JSONL,
// submits to the provider's batch API, and returns a job ID.
func (bm *BatchManager) SubmitBatch(requests []BatchRequest, backendName string) (*BatchJob, error) {
	backend := pool.Get(backendName)
	if backend == nil {
		return nil, fmt.Errorf("backend %q not found", backendName)
	}

	provider := detectBatchProvider(backend.Config.Type, backend.Config.URL)
	if provider == "" {
		return nil, fmt.Errorf("backend %q (type %s) does not support batch API", backendName, backend.Config.Type)
	}

	jobID := fmt.Sprintf("batch_%d", time.Now().UnixNano())

	// Write JSONL input file
	inputPath := filepath.Join(bm.dataDir, jobID+"_input.jsonl")
	if err := writeJSONL(inputPath, requests, provider); err != nil {
		return nil, fmt.Errorf("failed to write input file: %w", err)
	}

	job := &BatchJob{
		ID:           jobID,
		Provider:     provider,
		BackendName:  backendName,
		Status:       "pending",
		InputFile:    inputPath,
		OutputFile:   filepath.Join(bm.dataDir, jobID+"_output.jsonl"),
		RequestCount: len(requests),
		CreatedAt:    time.Now(),
	}

	// Submit to provider
	providerID, err := submitToProvider(backend, provider, inputPath, requests)
	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
	} else {
		job.ProviderID = providerID
		job.Status = "submitted"
	}

	bm.mu.Lock()
	bm.jobs[jobID] = job
	bm.mu.Unlock()
	bm.saveJobs()

	if job.Status == "submitted" {
		go bm.pollJob(job, backend)
	}

	return job, nil
}

// SetCallback sets a webhook URL that will receive POST notification
// with the job and results when the batch completes.
func (bm *BatchManager) SetCallback(jobID, callbackURL string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if job, ok := bm.jobs[jobID]; ok {
		job.CallbackURL = callbackURL
	}
}

// GetJob returns the current state of a batch job.
func (bm *BatchManager) GetJob(id string) *BatchJob {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.jobs[id]
}

// ListJobs returns all batch jobs.
func (bm *BatchManager) ListJobs() []*BatchJob {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	result := make([]*BatchJob, 0, len(bm.jobs))
	for _, j := range bm.jobs {
		result = append(result, j)
	}
	return result
}

// GetResults returns the results of a completed batch job.
func (bm *BatchManager) GetResults(id string) ([]BatchResult, error) {
	job := bm.GetJob(id)
	if job == nil {
		return nil, fmt.Errorf("job %q not found", id)
	}
	if job.Status != "completed" {
		return nil, fmt.Errorf("job %q status is %s, not completed", id, job.Status)
	}

	data, err := os.ReadFile(job.OutputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read output file: %w", err)
	}

	var results []BatchResult
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r BatchResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// detectBatchProvider determines which batch API to use based on backend config.
func detectBatchProvider(backendType, url string) string {
	switch backendType {
	case "openai":
		if strings.Contains(url, "openai.com") || strings.Contains(url, "azure") {
			return "openai"
		}
	case "gemini":
		return "gemini"
	}

	// Check URL patterns for provider detection
	urlLower := strings.ToLower(url)
	if strings.Contains(urlLower, "anthropic.com") {
		return "anthropic"
	}
	if strings.Contains(urlLower, "mistral.ai") {
		return "mistral"
	}
	if strings.Contains(urlLower, "groq.com") {
		return "groq"
	}
	if strings.Contains(urlLower, "together") {
		return "together"
	}
	if strings.Contains(urlLower, "fireworks") {
		return "fireworks"
	}

	return ""
}

// writeJSONL writes batch requests in the provider's expected format.
func writeJSONL(path string, requests []BatchRequest, provider string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, req := range requests {
		switch provider {
		case "anthropic":
			// Anthropic uses params directly, not method/url wrapper
			line := map[string]interface{}{
				"custom_id": req.CustomID,
				"params":    convertToAnthropicParams(req.Body),
			}
			enc.Encode(line)

		default:
			// OpenAI-compatible format (OpenAI, Groq, Together, Fireworks, Mistral)
			line := map[string]interface{}{
				"custom_id": req.CustomID,
				"method":    "POST",
				"url":       "/v1/chat/completions",
				"body":      req.Body,
			}
			enc.Encode(line)
		}
	}
	return nil
}

func convertToAnthropicParams(req ChatRequest) map[string]interface{} {
	params := map[string]interface{}{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
	}

	var messages []map[string]interface{}
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			if s, ok := msg.Content.(string); ok {
				params["system"] = s
			}
			continue
		}
		messages = append(messages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}
	params["messages"] = messages

	if req.Temperature != nil {
		params["temperature"] = *req.Temperature
	}
	return params
}

// submitToProvider submits the batch to the appropriate provider API.
func submitToProvider(backend *Backend, provider, inputPath string, requests []BatchRequest) (string, error) {
	switch provider {
	case "openai", "groq":
		return submitOpenAIBatch(backend, inputPath)
	case "anthropic":
		return submitAnthropicBatch(backend, requests)
	case "gemini":
		return submitGeminiBatch(backend, requests)
	case "mistral":
		return submitOpenAIBatch(backend, inputPath) // Mistral uses same format
	case "together":
		return submitOpenAIBatch(backend, inputPath) // Together uses same format
	case "fireworks":
		return submitFireworksBatch(backend, inputPath)
	default:
		return "", fmt.Errorf("unsupported batch provider: %s", provider)
	}
}

// submitOpenAIBatch uploads the JSONL file and creates a batch job (OpenAI, Groq, Together, Mistral).
func submitOpenAIBatch(backend *Backend, inputPath string) (string, error) {
	baseURL := strings.TrimSuffix(backend.Config.URL, "/v1")
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}

	// Step 1: Upload the file
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return "", err
	}

	// Create multipart form upload
	boundary := fmt.Sprintf("----BatchUpload%d", time.Now().UnixNano())
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Disposition: form-data; name=\"purpose\"\r\n\r\nbatch\r\n")
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"batch_input.jsonl\"\r\n")
	body.WriteString("Content-Type: application/jsonl\r\n\r\n")
	body.Write(data)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, _ := http.NewRequest("POST", baseURL+"/files", &body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", "Bearer "+backend.Config.APIKey)

	resp, err := llmClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("file upload failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("file upload returned %d: %s", resp.StatusCode, string(respBody))
	}

	var fileResp struct {
		ID string `json:"id"`
	}
	json.Unmarshal(respBody, &fileResp)
	if fileResp.ID == "" {
		return "", fmt.Errorf("no file ID in upload response")
	}

	// Step 2: Create the batch
	batchReq := map[string]interface{}{
		"input_file_id":     fileResp.ID,
		"endpoint":          "/v1/chat/completions",
		"completion_window": "24h",
	}
	batchBody, _ := json.Marshal(batchReq)

	req2, _ := http.NewRequest("POST", baseURL+"/batches", bytes.NewReader(batchBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+backend.Config.APIKey)

	resp2, err := llmClient.Do(req2)
	if err != nil {
		return "", fmt.Errorf("batch creation failed: %w", err)
	}
	defer resp2.Body.Close()
	respBody2, _ := io.ReadAll(resp2.Body)

	if resp2.StatusCode >= 400 {
		return "", fmt.Errorf("batch creation returned %d: %s", resp2.StatusCode, string(respBody2))
	}

	var batchResp struct {
		ID string `json:"id"`
	}
	json.Unmarshal(respBody2, &batchResp)
	return batchResp.ID, nil
}

// submitAnthropicBatch submits to Anthropic's Message Batches API (JSON body, not file upload).
func submitAnthropicBatch(backend *Backend, requests []BatchRequest) (string, error) {
	var batchRequests []map[string]interface{}
	for _, req := range requests {
		batchRequests = append(batchRequests, map[string]interface{}{
			"custom_id": req.CustomID,
			"params":    convertToAnthropicParams(req.Body),
		})
	}

	body, _ := json.Marshal(map[string]interface{}{
		"requests": batchRequests,
	})

	httpReq, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages/batches", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", backend.Config.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := llmClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic batch submit failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anthropic batch returned %d: %s", resp.StatusCode, string(respBody))
	}

	var batchResp struct {
		ID string `json:"id"`
	}
	json.Unmarshal(respBody, &batchResp)
	return batchResp.ID, nil
}

// submitGeminiBatch submits to Gemini's inline batch API.
func submitGeminiBatch(backend *Backend, requests []BatchRequest) (string, error) {
	model := backend.Config.ModelName
	url := backend.Config.URL + "/models/" + model + ":batchGenerateContent"

	var geminiRequests []map[string]interface{}
	for _, req := range requests {
		gr := buildGeminiRequest(&req.Body)
		geminiRequests = append(geminiRequests, map[string]interface{}{
			"key":     req.CustomID,
			"request": gr,
		})
	}

	body, _ := json.Marshal(map[string]interface{}{
		"requests": geminiRequests,
	})

	httpReq, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", backend.Config.APIKey)

	resp, err := llmClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("gemini batch submit failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gemini batch returned %d: %s", resp.StatusCode, string(respBody))
	}

	var batchResp struct {
		Name string `json:"name"`
	}
	json.Unmarshal(respBody, &batchResp)
	if batchResp.Name == "" {
		batchResp.Name = fmt.Sprintf("gemini-batch-%d", time.Now().UnixNano())
	}
	return batchResp.Name, nil
}

// submitFireworksBatch submits to Fireworks AI batch API.
func submitFireworksBatch(backend *Backend, inputPath string) (string, error) {
	// Fireworks uses a dataset upload + job creation flow
	// For now, use the same OpenAI-compatible flow (Fireworks supports it)
	return submitOpenAIBatch(backend, inputPath)
}

// pollJob polls the provider for batch job completion.
func (bm *BatchManager) pollJob(job *BatchJob, backend *Backend) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.After(25 * time.Hour)

	for {
		select {
		case <-timeout:
			bm.mu.Lock()
			job.Status = "expired"
			bm.mu.Unlock()
			return
		case <-ticker.C:
			status, outputFileID, err := checkProviderStatus(backend, job.Provider, job.ProviderID)
			if err != nil {
				logger.Printf("batch %s poll error: %v", job.ID, err)
				continue
			}

			bm.mu.Lock()
			switch status {
			case "completed":
				job.Status = "completed"
				now := time.Now()
				job.CompletedAt = &now
				callbackURL := job.CallbackURL

				if outputFileID != "" {
					go func() {
						bm.downloadResults(job, backend, outputFileID)
						bm.saveJobs()
						if callbackURL != "" {
							bm.deliverWebhook(job)
						}
					}()
				} else if callbackURL != "" {
					go bm.deliverWebhook(job)
				}
				bm.mu.Unlock()
				bm.saveJobs()
				logger.Printf("batch %s completed (%d requests, provider: %s)", job.ID, job.RequestCount, job.Provider)
				return

			case "failed", "expired", "cancelled":
				job.Status = status
				job.Error = "provider reported: " + status
				bm.mu.Unlock()
				bm.saveJobs()
				logger.Printf("batch %s %s", job.ID, status)
				return

			default:
				job.Status = "processing"
				bm.mu.Unlock()
			}
		}
	}
}

// checkProviderStatus checks the batch job status with the provider.
func checkProviderStatus(backend *Backend, provider, providerID string) (string, string, error) {
	switch provider {
	case "openai", "groq", "together", "mistral":
		return checkOpenAIBatchStatus(backend, providerID)
	case "anthropic":
		return checkAnthropicBatchStatus(backend, providerID)
	case "gemini":
		return checkGeminiBatchStatus(backend, providerID)
	default:
		return "", "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func checkOpenAIBatchStatus(backend *Backend, batchID string) (string, string, error) {
	baseURL := strings.TrimSuffix(backend.Config.URL, "/v1") + "/v1"
	req, _ := http.NewRequest("GET", baseURL+"/batches/"+batchID, nil)
	req.Header.Set("Authorization", "Bearer "+backend.Config.APIKey)

	resp, err := llmClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Status       string `json:"status"`
		OutputFileID string `json:"output_file_id"`
	}
	json.Unmarshal(body, &result)
	return result.Status, result.OutputFileID, nil
}

func checkAnthropicBatchStatus(backend *Backend, batchID string) (string, string, error) {
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/messages/batches/"+batchID, nil)
	req.Header.Set("x-api-key", backend.Config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := llmClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		ProcessingStatus string `json:"processing_status"`
	}
	json.Unmarshal(body, &result)

	// Map Anthropic statuses to our standard
	switch result.ProcessingStatus {
	case "ended":
		return "completed", batchID, nil // Anthropic uses streaming results, not file download
	case "canceling":
		return "cancelled", "", nil
	default:
		return "processing", "", nil
	}
}

func checkGeminiBatchStatus(backend *Backend, batchName string) (string, string, error) {
	url := backend.Config.URL + "/" + batchName
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("x-goog-api-key", backend.Config.APIKey)

	resp, err := llmClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		State string `json:"state"`
	}
	json.Unmarshal(body, &result)

	switch strings.ToUpper(result.State) {
	case "JOB_STATE_SUCCEEDED":
		return "completed", batchName, nil
	case "JOB_STATE_FAILED":
		return "failed", "", nil
	default:
		return "processing", "", nil
	}
}

// downloadResults downloads the output file from the provider.
func (bm *BatchManager) downloadResults(job *BatchJob, backend *Backend, outputFileID string) {
	switch job.Provider {
	case "openai", "groq", "together", "mistral":
		baseURL := strings.TrimSuffix(backend.Config.URL, "/v1") + "/v1"
		req, _ := http.NewRequest("GET", baseURL+"/files/"+outputFileID+"/content", nil)
		req.Header.Set("Authorization", "Bearer "+backend.Config.APIKey)

		resp, err := llmClient.Do(req)
		if err != nil {
			logger.Printf("batch %s download error: %v", job.ID, err)
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		os.WriteFile(job.OutputFile, data, 0644)

	case "anthropic":
		// Anthropic streams results from the batch endpoint
		req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/messages/batches/"+outputFileID+"/results", nil)
		req.Header.Set("x-api-key", backend.Config.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := llmClient.Do(req)
		if err != nil {
			logger.Printf("batch %s download error: %v", job.ID, err)
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		os.WriteFile(job.OutputFile, data, 0644)
	}

	logger.Printf("batch %s results downloaded to %s", job.ID, job.OutputFile)
}

// deliverWebhook POSTs the completed job and results to the callback URL.
func (bm *BatchManager) deliverWebhook(job *BatchJob) {
	results, err := bm.GetResults(job.ID)
	if err != nil {
		logger.Printf("batch %s webhook: failed to read results: %v", job.ID, err)
		results = nil
	}

	payload := map[string]interface{}{
		"event":   "batch.completed",
		"job":     job,
		"results": results,
	}
	body, _ := json.Marshal(payload)

	// Retry up to 3 times with backoff
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest("POST", job.CallbackURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Kronaxis-Event", "batch.completed")
		req.Header.Set("X-Kronaxis-Batch-ID", job.ID)

		resp, err := llmClient.Do(req)
		if err == nil && resp.StatusCode < 400 {
			resp.Body.Close()
			logger.Printf("batch %s webhook delivered to %s", job.ID, job.CallbackURL)
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Second)
	}
	logger.Printf("batch %s webhook delivery failed after 3 attempts", job.ID)
}

// ── API Handlers ─────────────────────────────────────────────────────

// handleBatchSubmit creates a new async batch job.
// POST /api/batch with body: {"backend": "cloud-fast", "requests": [...]}
func handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var req struct {
		Backend     string         `json:"backend"`
		Requests    []BatchRequest `json:"requests"`
		CallbackURL string         `json:"callback_url,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Backend == "" {
		writeErrorJSON(w, 400, "backend is required")
		return
	}
	if len(req.Requests) == 0 {
		writeErrorJSON(w, 400, "at least one request is required")
		return
	}

	job, err := batchMgr.SubmitBatch(req.Requests, req.Backend)
	if err != nil {
		writeErrorJSON(w, 400, err.Error())
		return
	}

	if req.CallbackURL != "" {
		batchMgr.SetCallback(job.ID, req.CallbackURL)
	}

	writeJSON(w, 201, job)
}

// handleBatchStatus returns the status of a batch job.
// GET /api/batch?id=batch_xxx
func handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if id == "" {
			// List all jobs
			writeJSON(w, 200, batchMgr.ListJobs())
			return
		}
		job := batchMgr.GetJob(id)
		if job == nil {
			writeErrorJSON(w, 404, "job not found")
			return
		}
		writeJSON(w, 200, job)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleBatchStream provides SSE updates for a batch job until completion.
// GET /api/batch/stream?id=batch_xxx
func handleBatchStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeErrorJSON(w, 400, "id parameter required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrorJSON(w, 500, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			job := batchMgr.GetJob(id)
			if job == nil {
				fmt.Fprintf(w, "event: error\ndata: {\"message\":\"job not found\"}\n\n")
				flusher.Flush()
				return
			}

			data, _ := json.Marshal(job)
			fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
			flusher.Flush()

			if job.Status == "completed" || job.Status == "failed" || job.Status == "expired" {
				// Send results if completed
				if job.Status == "completed" {
					results, err := batchMgr.GetResults(id)
					if err == nil {
						rdata, _ := json.Marshal(results)
						fmt.Fprintf(w, "event: results\ndata: %s\n\n", rdata)
						flusher.Flush()
					}
				}
				fmt.Fprintf(w, "event: done\ndata: {\"status\":\"%s\"}\n\n", job.Status)
				flusher.Flush()
				return
			}
		}
	}
}

// handleBatchResults returns the results of a completed batch job.
// GET /api/batch/results?id=batch_xxx
func handleBatchResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeErrorJSON(w, 400, "id parameter required")
		return
	}

	results, err := batchMgr.GetResults(id)
	if err != nil {
		writeErrorJSON(w, 400, err.Error())
		return
	}

	writeJSON(w, 200, results)
}
