package main

import "testing"

func TestJaccardSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical strings", "the quick brown fox", "the quick brown fox", 1.0},
		{"completely different", "alpha beta", "gamma delta", 0.0},
		{"both empty", "", "", 1.0},
		{"one empty", "alpha", "", 0.0},
		{"partial overlap 50%", "alpha beta gamma", "alpha beta delta", 0.5},
		{"case insensitive", "Hello World", "hello world", 1.0},
		{"punctuation ignored", "hello, world!", "hello world", 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jaccardSimilarity(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("jaccard(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestShadowRouter_AddTestAndMatch(t *testing.T) {
	sr := NewShadowRouter("")
	sr.AddTest(ShadowTest{
		Name:     "vanguard-vs-cheap",
		VariantA: "claude-sonnet",
		VariantB: "gemini-flash",
		SplitPct: 50,
		Match:    map[string]string{"service": "vanguard"},
	})

	if got := sr.MatchTest(RouteRequest{Service: "vanguard"}); got == nil {
		t.Fatal("expected match for vanguard")
	}
	if got := sr.MatchTest(RouteRequest{Service: "atlas"}); got != nil {
		t.Errorf("unexpected match for atlas: %+v", got)
	}
}

func TestShadowRouter_ShouldFire_PercentageGate(t *testing.T) {
	sr := NewShadowRouter("")
	hits := 0
	for i := 0; i < 1000; i++ {
		if sr.shouldFire(10) {
			hits++
		}
	}
	// Expect roughly 100 hits at 10%; allow ±50 tolerance for the
	// counter-based scheduler's evenness.
	if hits < 50 || hits > 150 {
		t.Errorf("10%% gate fired %d/1000, expected ~100", hits)
	}

	// 0% never fires.
	for i := 0; i < 100; i++ {
		if sr.shouldFire(0) {
			t.Fatal("0%% gate fired")
		}
	}
	// 100% always fires.
	for i := 0; i < 100; i++ {
		if !sr.shouldFire(100) {
			t.Fatal("100%% gate didn't fire")
		}
	}
}

func TestShadowRouter_RemoveTest(t *testing.T) {
	sr := NewShadowRouter("")
	sr.AddTest(ShadowTest{
		Name: "foo", VariantA: "a", VariantB: "b", SplitPct: 5, Match: nil,
	})
	if sr.MatchTest(RouteRequest{}) == nil {
		t.Fatal("test not found after add")
	}
	sr.RemoveTest("foo")
	if sr.MatchTest(RouteRequest{}) != nil {
		t.Error("test still present after remove")
	}
}
