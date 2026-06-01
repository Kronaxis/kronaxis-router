package main

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// CCR (compress-cache-retrieve) is a clean-room take on headroom's reversible
// compression idea (https://github.com/chopratejas/headroom, Apache-2.0): rather
// than lossily truncating an oversized block, we stash the original locally,
// drop a compact retrieval stub into the prompt, and let the model pull the
// original back on demand via the compress_retrieve tool / HTTP endpoint when it
// actually needs it. Most of the time it never asks, so the tokens are saved;
// when it does, nothing was lost. See NOTICE for attribution.
//
// The store is in-process, bounded, and FIFO-evicted. It is intended for the
// lifetime of a request window, not durable storage.

// ccrStore is the process-wide CCR store, initialised in main when CCR is
// enabled in config. nil when CCR is off.
var ccrStore *CCRStore

type ccrEntry struct {
	content string
}

// CCRStore is a thread-safe, capacity-bounded content store keyed by a short
// content hash. Identical content dedupes to the same id.
type CCRStore struct {
	mu      sync.Mutex
	cap     int
	entries map[string]ccrEntry
	order   []string // insertion order for FIFO eviction

	puts   uint64
	hits   uint64
	misses uint64
}

func newCCRStore(capacity int) *CCRStore {
	if capacity <= 0 {
		capacity = 1024
	}
	return &CCRStore{
		cap:     capacity,
		entries: make(map[string]ccrEntry, capacity),
	}
}

// ccrID returns the first 16 hex chars of the SHA-256 of content.
func ccrID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}

// Put stores content and returns its id. Identical content returns the existing
// id without growing the store.
func (c *CCRStore) Put(content string) string {
	id := ccrID(content)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	if _, ok := c.entries[id]; ok {
		return id
	}
	c.entries[id] = ccrEntry{content: content}
	c.order = append(c.order, id)
	for len(c.order) > c.cap {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	return id
}

// Get returns the stored content for id.
func (c *CCRStore) Get(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if ok {
		c.hits++
	} else {
		c.misses++
	}
	return e.content, ok
}

// Stats returns store counters for /metrics and diagnostics.
func (c *CCRStore) Stats() (entries int, puts, hits, misses uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.puts, c.hits, c.misses
}
