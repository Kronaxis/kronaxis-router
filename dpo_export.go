package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DPOPair is one Direct Preference Optimisation training row. Emitted
// when the cheap tier produces a schema-invalid response and the
// fallback tier produces a valid one. The cheap output is "rejected",
// the expensive output is "chosen".
//
// Output format is JSONL (one JSON object per line) compatible with
// HuggingFace's DPO trainer convention.
type DPOPair struct {
	Prompt   string                 `json:"prompt"`
	Rejected string                 `json:"rejected"`
	Chosen   string                 `json:"chosen"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// DPOExporter is a buffered, lock-free writer that appends DPO pairs to
// a JSONL file. A single writer goroutine drains the channel; callers
// never block (overflow drops with a metric increment).
//
// When the caller emits N pairs and N reaches a configured milestone, an
// audit event fires so operators know fresh fine-tuning data is ready.
type DPOExporter struct {
	enabled       atomic.Bool
	path          string
	ch            chan DPOPair
	dropped       atomic.Uint64
	written       atomic.Uint64
	milestoneSize uint64
	redactKeys    map[string]struct{}
	closeOnce     sync.Once
	stopCh        chan struct{}
}

// NewDPOExporter returns a configured exporter or nil if path is empty.
// path is expected to be a writable absolute path; the parent directory
// is created if missing. milestoneSize controls how often an audit
// event fires (e.g. 100 = audit event after every 100th pair). Set
// milestoneSize <= 0 to disable audit events.
func NewDPOExporter(path string, milestoneSize int, redactKeys []string) (*DPOExporter, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("dpo export dir: %w", err)
	}
	rk := map[string]struct{}{}
	for _, k := range redactKeys {
		rk[k] = struct{}{}
	}
	e := &DPOExporter{
		path:       path,
		ch:         make(chan DPOPair, 256),
		redactKeys: rk,
		stopCh:     make(chan struct{}),
	}
	if milestoneSize > 0 {
		e.milestoneSize = uint64(milestoneSize)
	}
	e.enabled.Store(true)
	go e.writerLoop()
	return e, nil
}

// Submit enqueues a pair for write. Non-blocking; on a full buffer the
// pair is dropped and the dropped counter increments. Caller cost is
// effectively a channel send (a few hundred nanoseconds).
func (e *DPOExporter) Submit(pair DPOPair) {
	if e == nil || !e.enabled.Load() {
		return
	}
	pair = e.redact(pair)
	select {
	case e.ch <- pair:
	default:
		e.dropped.Add(1)
	}
}

// Close stops the writer goroutine after flushing pending pairs.
func (e *DPOExporter) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.enabled.Store(false)
		close(e.stopCh)
		// Drain the channel: writer goroutine sees stopCh closed and
		// exits after writing whatever's left in ch.
	})
}

// Stats returns the marshallable counters.
func (e *DPOExporter) Stats() DPOStats {
	if e == nil {
		return DPOStats{Enabled: false}
	}
	return DPOStats{
		Enabled: e.enabled.Load(),
		Path:    e.path,
		Written: e.written.Load(),
		Dropped: e.dropped.Load(),
	}
}

// DPOStats is the public marshallable view.
type DPOStats struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path,omitempty"`
	Written uint64 `json:"written"`
	Dropped uint64 `json:"dropped"`
}

func (e *DPOExporter) writerLoop() {
	for {
		select {
		case pair, ok := <-e.ch:
			if !ok {
				return
			}
			e.appendOne(pair)
		case <-e.stopCh:
			// Drain remaining pairs.
			for {
				select {
				case pair := <-e.ch:
					e.appendOne(pair)
				default:
					return
				}
			}
		}
	}
}

func (e *DPOExporter) appendOne(pair DPOPair) {
	f, err := os.OpenFile(e.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		logger.Printf("dpo export open %s: %v", e.path, err)
		return
	}
	defer f.Close()
	if pair.Metadata == nil {
		pair.Metadata = map[string]interface{}{}
	}
	if _, ok := pair.Metadata["timestamp"]; !ok {
		pair.Metadata["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(pair)
	if err != nil {
		logger.Printf("dpo export marshal: %v", err)
		return
	}
	if _, err := f.Write(append(encoded, '\n')); err != nil {
		logger.Printf("dpo export write: %v", err)
		return
	}
	n := e.written.Add(1)
	if e.milestoneSize > 0 && n%e.milestoneSize == 0 {
		logger.Printf("dpo export milestone: %d pairs accumulated at %s", n, e.path)
		// Production deployments hook this up to their audit / alert pipe.
	}
}

// redact strips configured keys from the pair's prompt + metadata before
// it leaves the gateway. Useful for not leaking API keys or PII into a
// training dataset.
//
// Redaction rules:
//  - Any key in redactKeys is removed from Metadata if present.
//  - The prompt is not modified (PII redaction in raw text needs a
//    real classifier; out of scope for this layer). If you need that,
//    pre-process the prompt before calling Submit.
func (e *DPOExporter) redact(pair DPOPair) DPOPair {
	if len(e.redactKeys) == 0 || pair.Metadata == nil {
		return pair
	}
	for k := range e.redactKeys {
		delete(pair.Metadata, k)
	}
	return pair
}

// dpoExporter is the package-level handle, set in main.go from config.
var dpoExporter *DPOExporter

// ErrDPONotEnabled is returned by callers that try to query DPO state
// when the feature is off (no path configured).
var ErrDPONotEnabled = errors.New("DPO export not enabled")

// handleDPOStats serves GET /api/dpo for monitoring.
func handleDPOStatsBody() interface{} {
	if dpoExporter == nil {
		return DPOStats{Enabled: false}
	}
	return dpoExporter.Stats()
}

// SchemaValidator instance is package-level too so the proxy hot path
// can reach it without threading.
var schemaValidator = NewSchemaValidator()

// Wired into main.go once config tells us whether a DPO path is set.
func initDPOExporterFromEnv() {
	path := os.Getenv("DPO_EXPORT_PATH")
	if path == "" {
		return
	}
	milestone := 100
	exp, err := NewDPOExporter(path, milestone, []string{"api_key", "password"})
	if err != nil {
		logger.Printf("dpo export init failed: %v", err)
		return
	}
	dpoExporter = exp
	logger.Printf("DPO export enabled, path=%s milestone=%d", path, milestone)
}

// dpoCloseOnShutdown registers Close in the main shutdown path.
func dpoCloseOnShutdown(ctx context.Context) {
	if dpoExporter == nil {
		return
	}
	<-ctx.Done()
	dpoExporter.Close()
}
