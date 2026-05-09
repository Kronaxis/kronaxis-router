package main

import (
	"encoding/json"
	"net/http"
	"sync"
)

// liveBus fans out RequestRecords to subscribed SSE clients.
type liveBus struct {
	mu      sync.RWMutex
	subs    map[chan RequestRecord]struct{}
}

func newLiveBus() *liveBus {
	return &liveBus{subs: map[chan RequestRecord]struct{}{}}
}

func (b *liveBus) subscribe() chan RequestRecord {
	ch := make(chan RequestRecord, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *liveBus) unsubscribe(ch chan RequestRecord) {
	b.mu.Lock()
	delete(b.subs, ch)
	close(ch)
	b.mu.Unlock()
}

func (b *liveBus) publish(r RequestRecord) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- r:
		default:
			// drop on slow consumer
		}
	}
}

// auditWithLive wraps an AuditLogger and also publishes Request records to a
// liveBus for the web UI.
type auditWithLive struct {
	inner AuditLogger
	bus   *liveBus
}

func newAuditWithLive(inner AuditLogger, bus *liveBus) AuditLogger {
	return &auditWithLive{inner: inner, bus: bus}
}

func (a *auditWithLive) Event(kind string, fields map[string]any) {
	a.inner.Event(kind, fields)
}

func (a *auditWithLive) Request(r RequestRecord) {
	a.inner.Request(r)
	a.bus.publish(r)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.liveBus.subscribe()
	defer s.liveBus.unsubscribe(ch)

	enc := json.NewEncoder(w)
	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case rec, ok := <-ch:
			if !ok {
				return
			}
			_, _ = w.Write([]byte("data: "))
			_ = enc.Encode(rec)
			_, _ = w.Write([]byte("\n"))
			flusher.Flush()
		}
	}
}
