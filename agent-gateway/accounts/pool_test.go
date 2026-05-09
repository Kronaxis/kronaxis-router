package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func TestRoundRobin_ThreeAccounts(t *testing.T) {
	m := New()
	for i := 0; i < 3; i++ {
		acc := &Account{
			ID:         []string{"a", "b", "c"}[i],
			Pool:       "p1",
			Type:       TypeAPIKey,
			Credential: "k" + []string{"a", "b", "c"}[i],
		}
		if err := m.Add(acc); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		l, err := m.Checkout("p1")
		if err != nil {
			t.Fatalf("checkout %d: %v", i, err)
		}
		seen[l.Account().ID]++
		l.Release(OutcomeSuccess, "")
	}
	for _, id := range []string{"a", "b", "c"} {
		if seen[id] != 3 {
			t.Errorf("account %s used %d times, want 3", id, seen[id])
		}
	}
}

func TestCooldown_AppliesAndExpires(t *testing.T) {
	m := New()
	acc := &Account{
		ID:             "lonely",
		Pool:           "p1",
		Type:           TypeAPIKey,
		Credential:     "k",
		CooldownPolicy: CooldownPolicy{RateLimit: Duration(50 * time.Millisecond)},
	}
	if err := m.Add(acc); err != nil {
		t.Fatal(err)
	}
	l, err := m.Checkout("p1")
	if err != nil {
		t.Fatal(err)
	}
	l.Release(OutcomeRateLimit, "429")
	if _, err := m.Checkout("p1"); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected ErrPoolExhausted right after rate limit, got %v", err)
	}
	time.Sleep(70 * time.Millisecond)
	l2, err := m.Checkout("p1")
	if err != nil {
		t.Fatalf("expected checkout after cooldown, got %v", err)
	}
	l2.Release(OutcomeSuccess, "")
}

func TestPeek_ReportsSoonestExpiry(t *testing.T) {
	m := New()
	acc := &Account{
		ID:             "x",
		Pool:           "p1",
		Type:           TypeAPIKey,
		Credential:     "k",
		CooldownPolicy: CooldownPolicy{Transient: Duration(150 * time.Millisecond)},
	}
	_ = m.Add(acc)
	l, _ := m.Checkout("p1")
	l.Release(OutcomeTransient, "boom")
	_, rem, err := m.Peek("p1")
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("want ErrPoolExhausted, got %v", err)
	}
	if rem <= 0 || rem > 200*time.Millisecond {
		t.Fatalf("remaining %v out of expected band", rem)
	}
}

func TestUnknownPool(t *testing.T) {
	m := New()
	if _, err := m.Checkout("nope"); !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("want ErrUnknownPool, got %v", err)
	}
}

func TestConcurrentCheckout_NoDoubleIssue(t *testing.T) {
	m := New()
	for i := 0; i < 5; i++ {
		_ = m.Add(&Account{
			ID:         string(rune('a' + i)),
			Pool:       "p1",
			Type:       TypeAPIKey,
			Credential: "k",
		})
	}
	var wg sync.WaitGroup
	totalCheckouts := 200
	wg.Add(totalCheckouts)
	mu := sync.Mutex{}
	counts := map[string]int{}
	for i := 0; i < totalCheckouts; i++ {
		go func() {
			defer wg.Done()
			l, err := m.Checkout("p1")
			if err != nil {
				return
			}
			mu.Lock()
			counts[l.Account().ID]++
			mu.Unlock()
			l.Release(OutcomeSuccess, "")
		}()
	}
	wg.Wait()
	total := 0
	for _, c := range counts {
		total += c
	}
	if total != totalCheckouts {
		t.Errorf("got %d checkouts, want %d", total, totalCheckouts)
	}
}

func TestEnvInterpolation(t *testing.T) {
	t.Setenv("AGT_TEST_KEY_X", "secret123")
	a := &Account{ID: "x", Pool: "p", Type: TypeAPIKey, Credential: "${AGT_TEST_KEY_X}"}
	got, err := a.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret123" {
		t.Errorf("expected secret123, got %q", got)
	}
	a2 := &Account{ID: "y", Pool: "p", Type: TypeAPIKey, Credential: "${AGT_TEST_NOT_SET}"}
	if _, err := a2.Resolve(); err == nil {
		t.Fatal("expected missing-env error")
	}
}

func TestLoad_Directory(t *testing.T) {
	dir := t.TempDir()
	body := `accounts:
  - id: env-1
    pool: openai-api-key
    type: api-key
    credential: "${TEST_K1}"
    cooldown_policy:
      rate_limit: 1m
  - id: file-1
    pool: anthropic-oauth
    type: config-dir
    credential: "/path/to/account-1-credentials"
    cooldown_policy:
      rate_limit: 5h
      auth_failure: 5h
`
	if err := os.WriteFile(filepath.Join(dir, "test.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New()
	if err := m.Load(dir); err != nil {
		t.Fatal(err)
	}
	pools := m.Pools()
	if len(pools) != 2 {
		t.Fatalf("expected 2 pools, got %d (%v)", len(pools), pools)
	}
	st := m.Status()
	for _, p := range st {
		if p.AccountCount != 1 {
			t.Errorf("pool %s expected 1 account, got %d", p.Pool, p.AccountCount)
		}
	}
}

func TestValidate_ConfigDirRequiresAbsolute(t *testing.T) {
	a := &Account{ID: "x", Pool: "p", Type: TypeConfigDir, Credential: "relative/path"}
	err := a.Validate()
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestReset(t *testing.T) {
	m := New()
	for i := 0; i < 3; i++ {
		_ = m.Add(&Account{
			ID: string(rune('a' + i)), Pool: "p1", Type: TypeAPIKey, Credential: "k",
			CooldownPolicy: CooldownPolicy{Transient: Duration(time.Hour)},
		})
	}
	for i := 0; i < 3; i++ {
		l, _ := m.Checkout("p1")
		l.Release(OutcomeTransient, "x")
	}
	if _, err := m.Checkout("p1"); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected exhausted, got %v", err)
	}
	if n := m.Reset("p1"); n != 3 {
		t.Errorf("Reset returned %d, want 3", n)
	}
	if _, err := m.Checkout("p1"); err != nil {
		t.Fatalf("expected checkout after reset, got %v", err)
	}
}

func TestEnabledFlag_Skipped(t *testing.T) {
	m := New()
	_ = m.Add(&Account{ID: "off", Pool: "p1", Type: TypeAPIKey, Credential: "k", Enabled: boolPtr(false)})
	_ = m.Add(&Account{ID: "on", Pool: "p1", Type: TypeAPIKey, Credential: "k", Enabled: boolPtr(true)})
	for i := 0; i < 4; i++ {
		l, err := m.Checkout("p1")
		if err != nil {
			t.Fatal(err)
		}
		if l.Account().ID == "off" {
			t.Fatal("disabled account was issued")
		}
		l.Release(OutcomeSuccess, "")
	}
}
