package main

import (
	"strings"
	"testing"
	"time"
)

func TestNewSessionID_UniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "sess_") {
			t.Errorf("id %q missing sess_ prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

func TestSession_IsAlive(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		lastUsed   time.Time
		ttlSeconds int
		alive      bool
	}{
		{"fresh, 1h ttl", now, 3600, true},
		{"used 30 min ago, 1h ttl", now.Add(-30 * time.Minute), 3600, true},
		{"used 2h ago, 1h ttl", now.Add(-2 * time.Hour), 3600, false},
		{"zero ttl", now, 0, false},
		{"negative ttl", now, -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &Session{LastUsedAt: tc.lastUsed, TTLSeconds: tc.ttlSeconds}
			if got := sess.IsAlive(now); got != tc.alive {
				t.Errorf("IsAlive=%v, want %v", got, tc.alive)
			}
		})
	}
}

func TestRawOrNull(t *testing.T) {
	if string(rawOrNull(nil)) != "null" {
		t.Errorf("nil should serialize to null")
	}
	if string(rawOrNull([]byte(`{"a":1}`))) != `{"a":1}` {
		t.Errorf("non-empty should pass through")
	}
}
