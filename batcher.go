package main

import (
	"sync"
	"time"
)

// BatchEntry represents a single request waiting to be dispatched.
type BatchEntry struct {
	Body        []byte
	Parsed      *ChatRequest
	Route       RouteResult
	Meta        RouteRequest
	ResponseCh  chan *BatchResponse
	EnqueuedAt  time.Time
}

// BatchResponse carries the result back to the waiting handler.
type BatchResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
	Err        error
}

// Batcher collects non-streaming requests and dispatches them in groups.
type Batcher struct {
	config  BatchingConfig
	queues  map[string]*batchQueue // keyed by backend name
	mu      sync.RWMutex
}

type batchQueue struct {
	entries []*BatchEntry
	timer   *time.Timer
	mu      sync.Mutex
}

func newBatcher(config BatchingConfig) *Batcher {
	return &Batcher{
		config: config,
		queues: make(map[string]*batchQueue),
	}
}

func (b *Batcher) updateConfig(config BatchingConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = config
}

// ShouldBatch returns true if this request should go through the batcher.
func (b *Batcher) ShouldBatch(meta RouteRequest) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.config.Enabled {
		return false
	}
	if meta.Stream {
		return false
	}
	for _, bypass := range b.config.PriorityBypass {
		if meta.Priority == bypass {
			return false
		}
	}
	return true
}

// Enqueue adds a request to the batch queue for its target backend.
// Returns a channel that will receive the response when the batch is dispatched.
func (b *Batcher) Enqueue(entry *BatchEntry) <-chan *BatchResponse {
	backendName := entry.Route.Backend.Config.Name
	entry.ResponseCh = make(chan *BatchResponse, 1)
	entry.EnqueuedAt = time.Now()

	b.mu.Lock()
	q, ok := b.queues[backendName]
	if !ok {
		q = &batchQueue{}
		b.queues[backendName] = q
	}
	windowMS := b.config.WindowMS
	maxSize := b.config.MaxBatchSize

	// Background/bulk work gets a longer collection window
	if entry.Meta.Priority == "background" || entry.Meta.Priority == "bulk" {
		windowMS *= 2
	}
	b.mu.Unlock()

	q.mu.Lock()
	q.entries = append(q.entries, entry)

	// If batch is full, dispatch immediately
	if len(q.entries) >= maxSize {
		entries := q.entries
		q.entries = nil
		if q.timer != nil {
			q.timer.Stop()
			q.timer = nil
		}
		q.mu.Unlock()
		go dispatchBatch(entries)
		return entry.ResponseCh
	}

	// Start or reset the collection window timer
	if q.timer == nil {
		q.timer = time.AfterFunc(time.Duration(windowMS)*time.Millisecond, func() {
			q.mu.Lock()
			entries := q.entries
			q.entries = nil
			q.timer = nil
			q.mu.Unlock()
			if len(entries) > 0 {
				go dispatchBatch(entries)
			}
		})
	}

	q.mu.Unlock()
	return entry.ResponseCh
}

// dispatchBatch sends all entries in a batch concurrently.
// Each request is dispatched individually (vLLM handles GPU-side batching).
func dispatchBatch(entries []*BatchEntry) {
	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Add(1)
		go func(e *BatchEntry) {
			defer wg.Done()
			resp := dispatchSingle(e)
			e.ResponseCh <- resp
		}(entry)
	}
	wg.Wait()
}

// dispatchSingle forwards a single request to its routed backend.
func dispatchSingle(entry *BatchEntry) *BatchResponse {
	statusCode, headers, body, err := forwardToBackend(
		entry.Route.Backend,
		entry.Route.ModelName,
		entry.Body,
		entry.Parsed,
		entry.Meta,
	)
	return &BatchResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
		Err:        err,
	}
}
