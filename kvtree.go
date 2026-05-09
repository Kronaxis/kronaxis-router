package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// kvNode is a node in the per-backend prefix-hash tree.
//
// Each node represents one chunk of an incoming prompt's hash chain. A
// child node means "we've seen prompts whose first N+1 chunks match the
// path from root to here". lastSeenNano is bumped every time a prompt
// extends through this node, so we can evict stale prefixes.
//
// lastSeenNano is stored as atomic.Int64 (unix nanoseconds) so reads in
// LookupDepth and writes in Insert can run concurrently across siblings
// without holding the parent's mutex.
type kvNode struct {
	mu           sync.RWMutex
	children     map[uint64]*kvNode
	lastSeenNano atomic.Int64
}

func newKVNode(now time.Time) *kvNode {
	n := &kvNode{children: map[uint64]*kvNode{}}
	n.lastSeenNano.Store(now.UnixNano())
	return n
}

func (n *kvNode) lastSeen() time.Time {
	return time.Unix(0, n.lastSeenNano.Load())
}

func (n *kvNode) touch(now time.Time) {
	n.lastSeenNano.Store(now.UnixNano())
}

// KVTree is a single backend's view of which prompt prefixes it has
// likely cached in vLLM's KV. The tree is updated on every successful
// dispatch to this backend; it is consulted on every routing decision.
//
// Concurrency model:
//   - Read paths (LookupDepth) take node RLocks while walking; lock-free
//     map iteration would be unsafe.
//   - Write paths (Insert / Sweep) take node WLocks per node touched.
//   - The root mu protects the children map at the top level only;
//     deeper modifications are guarded per-node.
//
// Memory bound:
//   - Per-tree node cap (default 10_000) drops the oldest leaf chains
//     when exceeded. Sweep also runs periodically to evict by maxAge.
type KVTree struct {
	root      *kvNode
	maxAge    time.Duration
	maxNodes  int

	// approximate node counter; incremented on Insert, decremented on
	// Sweep. Not strictly accurate under heavy concurrent insert+sweep
	// but bounded enough for the cap heuristic.
	nodeCount atomicCounter
}

// NewKVTree creates an empty tree.
func NewKVTree(maxAge time.Duration, maxNodes int) *KVTree {
	if maxNodes <= 0 {
		maxNodes = 10_000
	}
	if maxAge <= 0 {
		maxAge = 10 * time.Minute
	}
	return &KVTree{
		root:     newKVNode(time.Now()),
		maxAge:   maxAge,
		maxNodes: maxNodes,
	}
}

// Insert walks the tree along chunkHashes, creating missing nodes and
// touching lastSeen on each. If the tree is over its node cap, Sweep
// is invoked synchronously before the insert.
func (t *KVTree) Insert(chunkHashes []uint64, now time.Time) {
	if t == nil || len(chunkHashes) == 0 {
		return
	}
	if t.nodeCount.Load() > int64(t.maxNodes) {
		t.Sweep(now)
	}
	cur := t.root
	for _, h := range chunkHashes {
		cur.mu.Lock()
		child, ok := cur.children[h]
		if !ok {
			child = newKVNode(now)
			cur.children[h] = child
			t.nodeCount.Add(1)
		}
		cur.mu.Unlock()
		// touch via atomic so concurrent LookupDepth on a sibling can
		// safely read this node's lastSeen without holding the parent.
		child.touch(now)
		cur = child
	}
}

// LookupDepth returns the count of prefix chunks matched, walking from
// the root. A return of N means "the first N hashes of chunkHashes are
// present and fresh in this tree". Callers compare across backends to
// pick the deepest match.
func (t *KVTree) LookupDepth(chunkHashes []uint64, now time.Time) int {
	if t == nil || len(chunkHashes) == 0 {
		return 0
	}
	depth := 0
	cur := t.root
	for _, h := range chunkHashes {
		cur.mu.RLock()
		child, ok := cur.children[h]
		cur.mu.RUnlock()
		if !ok {
			return depth
		}
		// Stale node: treat as miss.
		if now.Sub(child.lastSeen()) > t.maxAge {
			return depth
		}
		depth++
		cur = child
	}
	return depth
}

// Sweep evicts subtrees whose root node is older than maxAge, decrementing
// the node count by the number of nodes removed. Returns count removed.
func (t *KVTree) Sweep(now time.Time) int {
	if t == nil {
		return 0
	}
	cutoff := now.Add(-t.maxAge)
	removed := 0
	t.root.mu.Lock()
	defer t.root.mu.Unlock()
	for k, child := range t.root.children {
		dropped := pruneStale(child, cutoff)
		if isEmpty(child) && child.lastSeen().Before(cutoff) {
			delete(t.root.children, k)
			dropped++
		}
		removed += dropped
	}
	if removed > 0 {
		t.nodeCount.Add(int64(-removed))
	}
	return removed
}

// pruneStale recursively prunes a subtree, returning the number of nodes
// removed. The passed node itself is NOT removed; the caller decides that
// based on its own children and its own lastSeen.
func pruneStale(n *kvNode, cutoff time.Time) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	removed := 0
	for k, child := range n.children {
		dropped := pruneStale(child, cutoff)
		if isEmpty(child) && child.lastSeen().Before(cutoff) {
			delete(n.children, k)
			dropped++
		}
		removed += dropped
	}
	return removed
}

func isEmpty(n *kvNode) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.children) == 0
}

// Stats returns a coarse snapshot for /api/kv-trees.
func (t *KVTree) Stats() KVTreeStats {
	if t == nil {
		return KVTreeStats{}
	}
	return KVTreeStats{
		NodeCount: int(t.nodeCount.Load()),
		MaxNodes:  t.maxNodes,
		MaxAge:    t.maxAge.String(),
	}
}

// KVTreeStats is the marshallable view.
type KVTreeStats struct {
	NodeCount int    `json:"node_count"`
	MaxNodes  int    `json:"max_nodes"`
	MaxAge    string `json:"max_age"`
}

// atomicCounter is a tiny wrapper kept here to avoid pulling sync/atomic
// into every kvtree.go reader. Inlines fine.
type atomicCounter struct {
	v int64
	m sync.Mutex
}

func (c *atomicCounter) Add(d int64) int64 {
	c.m.Lock()
	defer c.m.Unlock()
	c.v += d
	return c.v
}

func (c *atomicCounter) Load() int64 {
	c.m.Lock()
	defer c.m.Unlock()
	return c.v
}
