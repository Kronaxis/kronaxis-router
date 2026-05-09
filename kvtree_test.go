package main

import (
	"sync"
	"testing"
	"time"
)

func TestKVTree_InsertAndLookup(t *testing.T) {
	tree := NewKVTree(10*time.Minute, 1000)
	now := time.Now()

	// Insert prefix [a, b, c]
	tree.Insert([]uint64{1, 2, 3}, now)

	// Look up exact match: depth 3
	if d := tree.LookupDepth([]uint64{1, 2, 3}, now); d != 3 {
		t.Errorf("exact match depth = %d, want 3", d)
	}

	// Partial match: depth 2
	if d := tree.LookupDepth([]uint64{1, 2, 99}, now); d != 2 {
		t.Errorf("partial match depth = %d, want 2", d)
	}

	// Diverge at first chunk: depth 0
	if d := tree.LookupDepth([]uint64{99, 2, 3}, now); d != 0 {
		t.Errorf("diverging chain depth = %d, want 0", d)
	}

	// Empty input: depth 0
	if d := tree.LookupDepth(nil, now); d != 0 {
		t.Errorf("empty input depth = %d, want 0", d)
	}
}

func TestKVTree_StaleTreatedAsMiss(t *testing.T) {
	tree := NewKVTree(50*time.Millisecond, 1000)
	t0 := time.Now()
	tree.Insert([]uint64{1, 2, 3}, t0)

	// 100 ms later, the prefix is past maxAge
	tNow := t0.Add(100 * time.Millisecond)
	if d := tree.LookupDepth([]uint64{1, 2, 3}, tNow); d != 0 {
		t.Errorf("stale depth = %d, want 0 (past maxAge)", d)
	}
}

func TestKVTree_ExtendsExistingPath(t *testing.T) {
	tree := NewKVTree(10*time.Minute, 1000)
	now := time.Now()

	tree.Insert([]uint64{1, 2}, now)
	tree.Insert([]uint64{1, 2, 3, 4}, now)

	// Both prefixes work; full one returns full depth.
	if d := tree.LookupDepth([]uint64{1, 2}, now); d != 2 {
		t.Errorf("short prefix depth = %d, want 2", d)
	}
	if d := tree.LookupDepth([]uint64{1, 2, 3, 4}, now); d != 4 {
		t.Errorf("long prefix depth = %d, want 4", d)
	}
}

func TestKVTree_SweepEvictsOldNodes(t *testing.T) {
	tree := NewKVTree(50*time.Millisecond, 1000)
	t0 := time.Now()
	tree.Insert([]uint64{1, 2, 3}, t0)
	tree.Insert([]uint64{4, 5, 6}, t0)
	if got := tree.nodeCount.Load(); got != 6 {
		t.Errorf("post-insert node count = %d, want 6", got)
	}

	tNow := t0.Add(100 * time.Millisecond)
	removed := tree.Sweep(tNow)
	if removed != 6 {
		t.Errorf("Sweep removed = %d, want 6", removed)
	}
	if got := tree.nodeCount.Load(); got != 0 {
		t.Errorf("post-sweep node count = %d, want 0", got)
	}
}

func TestKVTree_ConcurrentInsertAndLookup(t *testing.T) {
	tree := NewKVTree(10*time.Minute, 100_000)
	var wg sync.WaitGroup
	now := time.Now()

	// 50 writers, 50 readers, 200 ops each. Race detector catches data races.
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				tree.Insert([]uint64{uint64(id), uint64(j), uint64(j + 1)}, now)
			}
		}(i)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = tree.LookupDepth([]uint64{uint64(id), uint64(j)}, now)
			}
		}(i)
	}
	wg.Wait()
}

func TestChunkedPrefixHash_Stable(t *testing.T) {
	a := ChunkedPrefixHash("hello world this is a test prompt", 4)
	b := ChunkedPrefixHash("hello world this is a test prompt", 4)
	if len(a) == 0 {
		t.Fatal("hash chain is empty")
	}
	if len(a) != len(b) {
		t.Fatalf("len mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("chunk %d differs: %d vs %d", i, a[i], b[i])
		}
	}
}

func TestChunkedPrefixHash_PrefixMatchesExtend(t *testing.T) {
	a := ChunkedPrefixHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 4) // 32 chars = 2 chunks of 16
	b := ChunkedPrefixHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbb", 4)
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("empty hash chains")
	}
	// First chunks must match (same prefix); later chunks may differ.
	if a[0] != b[0] {
		t.Errorf("first-chunk mismatch despite identical prefix: %d vs %d", a[0], b[0])
	}
}

func TestKVIndex_RoundTrip(t *testing.T) {
	idx := NewKVIndex(4)
	idx.AddBackend("alpha", time.Hour, 1000)
	idx.AddBackend("beta", time.Hour, 1000)

	idx.Record("alpha", "the quick brown fox jumps over the lazy dog")
	idx.Record("beta", "completely different prompt content here")

	// alpha should match the fox prompt deeply; beta should not.
	candAlpha := RouteResult{Backend: &Backend{Config: BackendConfig{Name: "alpha"}}}
	candBeta := RouteResult{Backend: &Backend{Config: BackendConfig{Name: "beta"}}}
	in := []RouteResult{candBeta, candAlpha}

	out, depth := idx.ChooseByKVDepth(in, "the quick brown fox jumps")
	if depth == 0 {
		t.Fatalf("expected non-zero depth, got %d", depth)
	}
	// alpha must be ordered before beta in the output.
	if out[0].Backend.Config.Name != "alpha" {
		t.Errorf("alpha not promoted: order = %v",
			[]string{out[0].Backend.Config.Name, out[1].Backend.Config.Name})
	}
}
