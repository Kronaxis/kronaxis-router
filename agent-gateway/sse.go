package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

func newSSE(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, flusher: flusher}, nil
}

func (s *sseWriter) Send(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.w, "data: "); err != nil {
		return err
	}
	if _, err := s.w.Write(data); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, "\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// SendComment emits an SSE comment line ":<text>\n\n". Used for keepalive
// heartbeats. Comments are ignored by EventSource clients but keep idle
// connections open through proxies.
func (s *sseWriter) SendComment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.w, ":"+text+"\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *sseWriter) Done() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.WriteString(s.w, "data: [DONE]\n\n")
	s.flusher.Flush()
}

// startKeepalive runs a goroutine that emits an SSE comment every interval
// until ctx is cancelled. Returns a cancel function to stop the goroutine
// before the parent context completes (e.g. just before the final chunk).
func (s *sseWriter) startKeepalive(parent context.Context, interval time.Duration) context.CancelFunc {
	if interval <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.SendComment("keepalive"); err != nil {
					return
				}
			}
		}
	}()
	return cancel
}
