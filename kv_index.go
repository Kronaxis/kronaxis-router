package main

import (
	"sync"
	"time"
)

// KVIndex owns one KVTree per backend that has kv_pinning enabled, plus
// the chunk-hash logic that connects an incoming request to its prefix
// hash chain.
//
// Lifecycle:
//   - Created at startup from BackendConfig.KVPinning entries.
//   - Hot-reload of config: AddBackend / RemoveBackend keep it in sync.
//   - On every routing decision, ChooseByKVDepth is called to bias
//     candidates toward backends with a deep matching prefix.
//   - On every successful dispatch, Record is called to update the
//     chosen backend's tree.
//   - A periodic Sweep goroutine evicts stale subtrees.
type KVIndex struct {
	mu              sync.RWMutex
	trees           map[string]*KVTree
	hashChunkTokens int
}

// NewKVIndex returns an empty index. Backends are added via AddBackend
// when the index discovers (at config load) that they have kv_pinning.
func NewKVIndex(hashChunkTokens int) *KVIndex {
	if hashChunkTokens <= 0 {
		hashChunkTokens = 128
	}
	return &KVIndex{
		trees:           map[string]*KVTree{},
		hashChunkTokens: hashChunkTokens,
	}
}

// AddBackend registers a tree for the named backend. Idempotent.
func (idx *KVIndex) AddBackend(name string, maxAge time.Duration, maxNodes int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, ok := idx.trees[name]; ok {
		return
	}
	idx.trees[name] = NewKVTree(maxAge, maxNodes)
}

// RemoveBackend drops a tree.
func (idx *KVIndex) RemoveBackend(name string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.trees, name)
}

// HasBackend reports whether the named backend is KV-pinning.
func (idx *KVIndex) HasBackend(name string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	_, ok := idx.trees[name]
	return ok
}

// ChooseByKVDepth reorders candidates so the deepest prefix match comes
// first. If no candidate has any prefix match, the input order is
// preserved (caller will then apply least-connections / RR).
//
// Returns (reordered candidates, depth of best match). A depth of 0
// means "no candidate had any matching prefix"; the caller should
// treat that as a cache-miss and fall through to its existing logic.
func (idx *KVIndex) ChooseByKVDepth(candidates []RouteResult, prompt string) ([]RouteResult, int) {
	if idx == nil || len(candidates) == 0 || prompt == "" {
		return candidates, 0
	}
	hashes := ChunkedPrefixHash(prompt, idx.hashChunkTokens)
	if len(hashes) == 0 {
		return candidates, 0
	}
	now := time.Now()

	idx.mu.RLock()
	type scored struct {
		idx   int
		depth int
	}
	scores := make([]scored, len(candidates))
	maxDepth := 0
	for i, c := range candidates {
		tree, ok := idx.trees[c.Backend.Config.Name]
		if !ok {
			scores[i] = scored{i, 0}
			continue
		}
		d := tree.LookupDepth(hashes, now)
		scores[i] = scored{i, d}
		if d > maxDepth {
			maxDepth = d
		}
	}
	idx.mu.RUnlock()

	if maxDepth == 0 {
		return candidates, 0
	}

	// Stable partition: matchers (highest depth) first, non-matchers after.
	// We don't fully sort by depth because we want least-connections
	// tiebreakers within the matched group, which the caller applies.
	//
	// Actually we do want order-by-depth-desc so the *deepest* match wins
	// outright; tiebreakers happen at equal depths.
	result := make([]RouteResult, 0, len(candidates))
	// Two-pass: emit by descending depth, preserving input order within
	// each depth bucket so the caller's RR logic is deterministic.
	for d := maxDepth; d >= 1; d-- {
		for _, s := range scores {
			if s.depth == d {
				result = append(result, candidates[s.idx])
			}
		}
	}
	for _, s := range scores {
		if s.depth == 0 {
			result = append(result, candidates[s.idx])
		}
	}
	return result, maxDepth
}

// Record updates the chosen backend's tree with the prompt's prefix hash
// chain. Called after a successful dispatch (i.e. the backend actually
// processed the request and its KV is now warm for this prefix).
func (idx *KVIndex) Record(backendName, prompt string) {
	if idx == nil || backendName == "" || prompt == "" {
		return
	}
	idx.mu.RLock()
	tree, ok := idx.trees[backendName]
	idx.mu.RUnlock()
	if !ok {
		return
	}
	hashes := ChunkedPrefixHash(prompt, idx.hashChunkTokens)
	if len(hashes) == 0 {
		return
	}
	tree.Insert(hashes, time.Now())
}

// Stats returns a snapshot for /api/kv-trees.
func (idx *KVIndex) Stats() map[string]KVTreeStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string]KVTreeStats, len(idx.trees))
	for name, tree := range idx.trees {
		out[name] = tree.Stats()
	}
	return out
}

// SweepAll triggers a sweep on every tree. Called on a ticker.
func (idx *KVIndex) SweepAll() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	now := time.Now()
	total := 0
	for _, tree := range idx.trees {
		total += tree.Sweep(now)
	}
	return total
}
