package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyAccountError(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		wantKind string
		wantCool time.Duration
	}{
		{"", "", "", 0},
		{"anthropic 429: rate_limit_error", "anthropic", "rate_limit", 5 * time.Minute},
		{"openai 429 Too Many Requests", "openai", "rate_limit", 5 * time.Minute},
		// claude-cli OAuth: 5h cooldown
		{"5-hour usage limit reached", "claude-cli", "rate_limit", 5 * time.Hour},
		{"429 too many requests", "claude-cli", "rate_limit", 5 * time.Hour},
		{"anthropic 401: invalid_api_key", "anthropic", "auth", 0},
		{"permission_error: no access", "anthropic", "auth", 0},
		{"insufficient_credit", "anthropic", "credit", 0},
		{"credit_balance_too_low", "anthropic", "credit", 0},
		{"http 503 service unavailable", "anthropic", "transient", 30 * time.Second},
		{"some unrelated error", "anthropic", "", 0},
	}
	for _, c := range cases {
		k, d := classifyAccountError(c.in, c.provider)
		if k != c.wantKind || d != c.wantCool {
			t.Errorf("classifyAccountError(%q, %q) = (%q,%v), want (%q,%v)",
				c.in, c.provider, k, d, c.wantKind, c.wantCool)
		}
	}
}

func TestRecordOutcome(t *testing.T) {
	a := &Account{ID: "test", Provider: "anthropic"}

	recordOutcome(a, nil)
	if a.successCount.Load() != 1 {
		t.Errorf("success not counted: %d", a.successCount.Load())
	}
	if !a.IsEnabled() {
		t.Error("account disabled after success")
	}

	recordOutcome(a, errors.New("some unrelated thing"))
	if a.failureCount.Load() != 1 {
		t.Errorf("failure not counted: %d", a.failureCount.Load())
	}
	if !a.IsEnabled() {
		t.Error("account disabled on unrelated failure")
	}

	recordOutcome(a, errors.New("anthropic 429 too many requests"))
	if a.IsEnabled() {
		t.Error("account still enabled after 429")
	}

	b := &Account{ID: "test2", Provider: "anthropic"}
	recordOutcome(b, errors.New("invalid_api_key"))
	if b.IsEnabled() {
		t.Error("account still enabled after auth error")
	}
}

func TestPickRoundRobin(t *testing.T) {
	p := &AuthPool{
		accounts: map[string][]*Account{
			"anthropic": {
				{ID: "a", Provider: "anthropic"},
				{ID: "b", Provider: "anthropic"},
				{ID: "c", Provider: "anthropic"},
			},
		},
		rr: map[string]*atomic.Uint64{"anthropic": {}},
	}

	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		acc, err := p.Pick("anthropic", "")
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		seen[acc.ID]++
	}
	if seen["a"] != 3 || seen["b"] != 3 || seen["c"] != 3 {
		t.Errorf("round-robin distribution off: %+v", seen)
	}

	p.accounts["anthropic"][0].Disable("test", time.Hour)
	seen = map[string]int{}
	for i := 0; i < 6; i++ {
		acc, err := p.Pick("anthropic", "")
		if err != nil {
			t.Fatalf("pick after disable %d: %v", i, err)
		}
		seen[acc.ID]++
	}
	if seen["a"] != 0 {
		t.Errorf("disabled account 'a' was picked %d times", seen["a"])
	}
}

func TestPickByID(t *testing.T) {
	p := &AuthPool{
		accounts: map[string][]*Account{
			"anthropic": {
				{ID: "x", Provider: "anthropic"},
				{ID: "y", Provider: "anthropic"},
			},
		},
		rr: map[string]*atomic.Uint64{"anthropic": {}},
	}
	acc, err := p.Pick("anthropic", "y")
	if err != nil || acc.ID != "y" {
		t.Errorf("pick id=y: got (%v,%v)", acc, err)
	}
	if _, err := p.Pick("anthropic", "missing"); err == nil {
		t.Error("expected error for missing id")
	}
}
