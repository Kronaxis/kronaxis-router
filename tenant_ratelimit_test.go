package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenBucket_TryAcquireBurst(t *testing.T) {
	tb := NewTokenBucket(5, 1.0) // capacity 5, refill 1/sec
	for i := 0; i < 5; i++ {
		if !tb.TryAcquire(1) {
			t.Fatalf("burst token %d should succeed", i)
		}
	}
	if tb.TryAcquire(1) {
		t.Fatal("6th token should fail (bucket empty)")
	}
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	tb := NewTokenBucket(2, 10.0) // capacity 2, refill 10/sec (100ms each)
	if !tb.TryAcquire(2) {
		t.Fatal("burst of 2 should succeed")
	}
	if tb.TryAcquire(1) {
		t.Fatal("should be empty")
	}
	// Wait 250ms => 2.5 tokens refilled, cap at 2
	time.Sleep(250 * time.Millisecond)
	if !tb.TryAcquire(2) {
		t.Fatal("should have refilled to cap")
	}
	if tb.TryAcquire(1) {
		t.Fatal("should be empty again")
	}
}

func TestTokenBucket_CapsAtMax(t *testing.T) {
	tb := NewTokenBucket(3, 100.0)
	time.Sleep(50 * time.Millisecond) // refill would be 5 tokens, must cap at 3
	if got := tb.Tokens(); got > 3.0+0.001 {
		t.Fatalf("bucket exceeded cap: got %g", got)
	}
}

func TestTokenBucket_AcquireBlocksThenSucceeds(t *testing.T) {
	tb := NewTokenBucket(1, 20.0) // capacity 1, refill 20/sec
	if !tb.TryAcquire(1) {
		t.Fatal("first should succeed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := tb.Acquire(ctx, 1); err != nil {
		t.Fatalf("acquire should succeed within timeout: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("acquire returned too fast (%v); should have waited for refill", elapsed)
	}
}

func TestTokenBucket_AcquireRespectsContextCancel(t *testing.T) {
	tb := NewTokenBucket(1, 0.1) // capacity 1, refill 1 per 10 sec
	if !tb.TryAcquire(1) {
		t.Fatal("first should succeed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := tb.Acquire(ctx, 1)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// Either ErrRateLimited (upfront refuse since wait > deadline) or
	// context.DeadlineExceeded (if the timer fires first). Both are
	// acceptable -- the contract is "do not return nil".
	if !errors.Is(err, ErrRateLimited) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTokenBucket_ConcurrentSafety(t *testing.T) {
	tb := NewTokenBucket(100, 1000.0)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tb.TryAcquire(1) {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	// At least the initial 100 burst should succeed; possibly more due
	// to refill during execution.
	if successes.Load() < 100 {
		t.Fatalf("expected at least 100 successes from burst, got %d", successes.Load())
	}
}

func TestTokenBucket_ZeroAndNegative(t *testing.T) {
	tb := NewTokenBucket(0, 0) // both invalid; should default to 1/1
	if tb.maxTokens != 1 || tb.refillPerSec != 1 {
		t.Fatalf("zero capacity/rate should default to 1/1, got %g/%g", tb.maxTokens, tb.refillPerSec)
	}
	if !tb.TryAcquire(0) {
		t.Fatal("acquire(0) should always succeed")
	}
	if !tb.TryAcquire(-1) {
		t.Fatal("acquire(negative) should always succeed (no-op)")
	}
}

func TestTenantRateLimitConfig_Defaults(t *testing.T) {
	cfg := TenantRateLimitConfig{}.WithDefaults()
	if cfg.DefaultRPM != 60 {
		t.Errorf("DefaultRPM default = %g, want 60", cfg.DefaultRPM)
	}
	if cfg.DefaultBurst != 10 {
		t.Errorf("DefaultBurst default = %d, want 10", cfg.DefaultBurst)
	}
	if cfg.UnattributedRPM != 30 {
		t.Errorf("UnattributedRPM default = %g, want 30", cfg.UnattributedRPM)
	}
	if cfg.UnattributedBurst != 5 {
		t.Errorf("UnattributedBurst default = %d, want 5", cfg.UnattributedBurst)
	}
}

func TestTenantRateLimiter_BypassBypassesEverything(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:    1, // tiny cap; would block after 1 req
		DefaultBurst:  1,
		BypassTenants: []string{"controld", "billing-agentd", "System"},
	})
	for i := 0; i < 50; i++ {
		allowed, _ := rl.Allow("controld")
		if !allowed {
			t.Fatalf("bypass tenant request %d denied", i)
		}
	}
	// Case-insensitivity check
	for i := 0; i < 10; i++ {
		allowed, _ := rl.Allow("system")
		if !allowed {
			t.Fatalf("case-insensitive bypass request %d denied", i)
		}
	}
}

func TestTenantRateLimiter_DefaultLimitEnforced(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 3,
	})
	// Burst of 3 should pass.
	for i := 0; i < 3; i++ {
		allowed, _ := rl.Allow("tenant-a")
		if !allowed {
			t.Fatalf("burst req %d denied", i)
		}
	}
	// 4th should fail with Retry-After.
	allowed, retry := rl.Allow("tenant-a")
	if allowed {
		t.Fatal("4th req should be rate-limited")
	}
	if retry < time.Second {
		t.Errorf("Retry-After too short: %v", retry)
	}
}

func TestTenantRateLimiter_OneTenantDoesNotStarveAnother(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 3,
	})
	// Tenant A burns its burst.
	for i := 0; i < 3; i++ {
		allowed, _ := rl.Allow("tenant-a")
		if !allowed {
			t.Fatalf("A burst %d denied", i)
		}
	}
	allowed, _ := rl.Allow("tenant-a")
	if allowed {
		t.Fatal("A should be rate-limited after burst")
	}
	// Tenant B should still have its full burst regardless of A's state.
	for i := 0; i < 3; i++ {
		allowed, _ := rl.Allow("tenant-b")
		if !allowed {
			t.Fatalf("B burst %d denied while A is rate-limited (starvation!)", i)
		}
	}
}

func TestTenantRateLimiter_PerTenantOverrideRaisesCap(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 1,
		Overrides: map[string]TenantOverride{
			"premium-tenant": {RPM: 600, Burst: 20},
		},
	})
	// Default tenant: 1 then 429.
	allowedA, _ := rl.Allow("normal-tenant")
	allowedB, _ := rl.Allow("normal-tenant")
	if !allowedA || allowedB {
		t.Fatalf("normal tenant should have burst=1, got %v/%v", allowedA, allowedB)
	}
	// Premium gets 20.
	for i := 0; i < 20; i++ {
		allowed, _ := rl.Allow("premium-tenant")
		if !allowed {
			t.Fatalf("premium burst %d denied", i)
		}
	}
	allowed, _ := rl.Allow("premium-tenant")
	if allowed {
		t.Fatal("premium 21st should be rate-limited")
	}
}

func TestTenantRateLimiter_UnattributedUsesSeparateBucket(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:        60,
		DefaultBurst:      10,
		UnattributedRPM:   60,
		UnattributedBurst: 2,
	})
	// Two empty-tenant calls (anonymous), then 429.
	allowedA, _ := rl.Allow("")
	allowedB, _ := rl.Allow("")
	allowedC, _ := rl.Allow("")
	if !allowedA || !allowedB || allowedC {
		t.Fatalf("unattributed burst should be 2: %v/%v/%v", allowedA, allowedB, allowedC)
	}
	// Real tenant unaffected.
	for i := 0; i < 10; i++ {
		allowed, _ := rl.Allow("tenant-a")
		if !allowed {
			t.Fatalf("real tenant denied at burst %d while unattributed exhausted", i)
		}
	}
}

func TestTenantRateLimiter_DisabledShortCircuits(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 1,
		Disabled:     true,
	})
	for i := 0; i < 100; i++ {
		allowed, _ := rl.Allow("tenant-a")
		if !allowed {
			t.Fatalf("disabled limiter denied req %d", i)
		}
	}
}

func TestTenantRateLimiter_UpdateConfigResetsBuckets(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 1,
	})
	allowed, _ := rl.Allow("tenant-a")
	if !allowed {
		t.Fatal("first should pass")
	}
	allowed, _ = rl.Allow("tenant-a")
	if allowed {
		t.Fatal("second should be denied (burst exhausted)")
	}
	// Raise the cap and reset.
	rl.UpdateConfig(TenantRateLimitConfig{DefaultRPM: 6000, DefaultBurst: 100})
	for i := 0; i < 100; i++ {
		allowed, _ := rl.Allow("tenant-a")
		if !allowed {
			t.Fatalf("after UpdateConfig burst %d denied", i)
		}
	}
}

// Integration test: 50 concurrent reqs from a runaway tenant + 5 from
// a quiet tenant -- quiet tenant must not be starved.
func TestTenantRateLimiter_RunawayTenantDoesNotStarveOthers(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60, // 1/sec sustained
		DefaultBurst: 5,
	})

	var (
		runawayAllowed atomic.Int32
		runawayDenied  atomic.Int32
		quietAllowed   atomic.Int32
		quietDenied    atomic.Int32
		wg             sync.WaitGroup
	)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _ := rl.Allow("runaway-tenant")
			if allowed {
				runawayAllowed.Add(1)
			} else {
				runawayDenied.Add(1)
			}
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _ := rl.Allow("quiet-tenant")
			if allowed {
				quietAllowed.Add(1)
			} else {
				quietDenied.Add(1)
			}
		}()
	}
	wg.Wait()

	// Runaway: at most burst+a-tiny-bit-of-refill allowed, lots denied.
	if runawayAllowed.Load() > 10 {
		t.Errorf("runaway tenant allowed too many: %d (expected ≤10)", runawayAllowed.Load())
	}
	if runawayDenied.Load() < 30 {
		t.Errorf("runaway tenant denied too few: %d (expected ≥30)", runawayDenied.Load())
	}
	// Quiet: ALL 5 should pass.
	if quietAllowed.Load() != 5 {
		t.Errorf("quiet tenant starved: allowed=%d denied=%d", quietAllowed.Load(), quietDenied.Load())
	}
}

func TestExtractTenant_HeaderPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "tenant header wins",
			headers: map[string]string{"X-Kronaxis-Tenant-ID": "acme", "X-Kronaxis-Service": "bulk-extractor"},
			want:    "acme",
		},
		{
			name:    "service header is fallback",
			headers: map[string]string{"X-Kronaxis-Service": "bulk-extractor"},
			want:    "bulk-extractor",
		},
		{
			name:    "no headers means empty (unattributed)",
			headers: map[string]string{},
			want:    "",
		},
		{
			name:    "whitespace stripped",
			headers: map[string]string{"X-Kronaxis-Tenant-ID": "  acme  "},
			want:    "acme",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := extractTenant(r); got != tc.want {
				t.Errorf("extractTenant() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTenantRateLimitMiddleware_Returns429OnBurst(t *testing.T) {
	// Inject a small limiter into the package-level var for the
	// middleware to see.
	prev := tenantRateLim
	defer func() { tenantRateLim = prev }()
	tenantRateLim = NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 2,
	})

	called := atomic.Int32{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(200)
	})
	h := tenantRateLimitMiddleware(next)

	mkReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("X-Kronaxis-Tenant-ID", "burst-tenant")
		return r
	}

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, mkReq())
		if w.Code != 200 {
			t.Fatalf("burst req %d: want 200, got %d", i, w.Code)
		}
	}
	// 3rd should be 429.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, mkReq())
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd req: want 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing on 429")
	}
	if called.Load() != 2 {
		t.Errorf("next handler called %d times, want 2", called.Load())
	}
}

func TestTenantRateLimitMiddleware_NonMeteredPathBypasses(t *testing.T) {
	prev := tenantRateLim
	defer func() { tenantRateLim = prev }()
	tenantRateLim = NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 1,
	})

	called := atomic.Int32{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(200)
	})
	h := tenantRateLimitMiddleware(next)

	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		r.Header.Set("X-Kronaxis-Tenant-ID", "spammer")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("/health %d: want 200, got %d", i, w.Code)
		}
	}
	if called.Load() != 10 {
		t.Errorf("/health should never be rate-limited; called=%d", called.Load())
	}
}

func TestTenantRateLimiter_SnapshotReportsCounters(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 1,
	})
	_, _ = rl.Allow("tenant-a") // allowed
	_, _ = rl.Allow("tenant-a") // limited
	_, _ = rl.Allow("tenant-b") // allowed

	allowed, limited, tokens := rl.snapshot()
	if allowed["tenant-a"] != 1 {
		t.Errorf("tenant-a allowed = %d, want 1", allowed["tenant-a"])
	}
	if limited["tenant-a"] != 1 {
		t.Errorf("tenant-a limited = %d, want 1", limited["tenant-a"])
	}
	if allowed["tenant-b"] != 1 {
		t.Errorf("tenant-b allowed = %d, want 1", allowed["tenant-b"])
	}
	if _, ok := tokens["tenant-a"]; !ok {
		t.Error("tokens snapshot missing tenant-a")
	}
}

func TestTenantRateLimitConfig_FromYAML(t *testing.T) {
	yamlData := []byte(`
tenant_rate_limits:
  default_rpm: 120
  default_burst: 20
  unattributed_rpm: 15
  unattributed_burst: 3
  bypass_tenants:
    - controld
    - billing-agentd
  overrides:
    premium-tenant:
      rpm: 600
      burst: 50
`)
	cfg, err := loadConfigFromBytes(yamlData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.TenantRateLimits.DefaultRPM != 120 {
		t.Errorf("DefaultRPM = %g, want 120", cfg.TenantRateLimits.DefaultRPM)
	}
	if cfg.TenantRateLimits.DefaultBurst != 20 {
		t.Errorf("DefaultBurst = %d, want 20", cfg.TenantRateLimits.DefaultBurst)
	}
	if len(cfg.TenantRateLimits.BypassTenants) != 2 {
		t.Errorf("BypassTenants len = %d, want 2", len(cfg.TenantRateLimits.BypassTenants))
	}
	ov, ok := cfg.TenantRateLimits.Overrides["premium-tenant"]
	if !ok {
		t.Fatal("override premium-tenant missing")
	}
	if ov.RPM != 600 || ov.Burst != 50 {
		t.Errorf("override = %+v, want {600 50}", ov)
	}
}

// Sanity check: the package compiles + the middleware's "shouldMeter"
// rule covers exactly the endpoints we expect.
func TestShouldMeter_Coverage(t *testing.T) {
	metered := []string{"/v1/chat/completions", "/v1/video/generate"}
	notMetered := []string{"/health", "/metrics", "/api/costs", "/", "/v1/sessions", "/v1/video/foo"}
	for _, p := range metered {
		r := httptest.NewRequest(http.MethodPost, p, nil)
		if !shouldMeter(r) {
			t.Errorf("%s should be metered", p)
		}
	}
	for _, p := range notMetered {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if shouldMeter(r) {
			t.Errorf("%s should NOT be metered", p)
		}
	}
}

// String-format invariant: rejection error mentions the tenant + Retry-After seconds.
func TestTenantRateLimitMiddleware_ErrorBodyFormat(t *testing.T) {
	prev := tenantRateLim
	defer func() { tenantRateLim = prev }()
	tenantRateLim = NewTenantRateLimiter(TenantRateLimitConfig{
		DefaultRPM:   60,
		DefaultBurst: 1,
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := tenantRateLimitMiddleware(next)
	mk := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("X-Kronaxis-Tenant-ID", "needy")
		return r
	}
	// burn the burst
	h.ServeHTTP(httptest.NewRecorder(), mk())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, mk())
	if w.Code != 429 {
		t.Fatalf("want 429, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	// JSON-escaped tenant name shows up as \"needy\" inside the message string.
	if !strings.Contains(body, `needy`) {
		t.Errorf("body should mention tenant: %s", body)
	}
}

// Make sure the integration smoke uses the same package-level singleton
// initialisation that production uses. We don't import a separate
// constructor.
func TestPackageInit_NoTenantRateLimWhenUnset(t *testing.T) {
	// Simply asserting that nil tenantRateLim doesn't panic the
	// middleware; main() always initialises it but tests of other files
	// may not.
	prev := tenantRateLim
	tenantRateLim = nil
	defer func() { tenantRateLim = prev }()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := tenantRateLimitMiddleware(next)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Kronaxis-Tenant-ID", "anyone")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("nil limiter should pass through; got %d", w.Code)
	}
}

// Fuzz-ish: lots of distinct tenants don't crash the registry.
func TestTenantRateLimiter_ManyTenants(t *testing.T) {
	rl := NewTenantRateLimiter(TenantRateLimitConfig{DefaultRPM: 60, DefaultBurst: 1})
	for i := 0; i < 1000; i++ {
		_, _ = rl.Allow(fmt.Sprintf("tenant-%d", i))
	}
	allowed, _, _ := rl.snapshot()
	if len(allowed) < 900 {
		t.Errorf("expected ~1000 buckets, got %d", len(allowed))
	}
}
