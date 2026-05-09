package main

import (
	"sync"
	"time"
)

// profileLimiter enforces per-profile concurrency + rate-per-minute limits
// on top of the gateway's global semaphore. Each profile gets its own
// bounded channel for concurrency and its own token bucket for rate.
//
// Concurrency works like the global semaphore: send {} into the channel
// to acquire, receive from it to release.
//
// Rate-per-minute uses a refill-on-demand bucket: each time Acquire is
// called we top up tokens proportional to elapsed time, then take one.
// If no tokens, we return false and the caller can decide to 429 or wait.
type profileLimiter struct {
	concurrency chan struct{}
	rateMu      sync.Mutex
	rateTokens  float64
	rateMax     float64 // requests per minute
	rateLast    time.Time
}

// newProfileLimiter returns a limiter with the given caps. Either cap can
// be 0 to disable that dimension.
func newProfileLimiter(maxConcurrent, ratePerMinute int) *profileLimiter {
	pl := &profileLimiter{}
	if maxConcurrent > 0 {
		pl.concurrency = make(chan struct{}, maxConcurrent)
	}
	if ratePerMinute > 0 {
		pl.rateMax = float64(ratePerMinute)
		pl.rateTokens = pl.rateMax
		pl.rateLast = time.Now()
	}
	return pl
}

// Acquire takes a concurrency slot (blocking) and a rate token (returns
// false if the bucket is empty). If concurrency is unset, the slot is a
// no-op; if rate is unset, true is always returned.
//
// Returns release func + ok flag. release MUST be called even when ok=false
// because it is a no-op for empty channels.
func (pl *profileLimiter) Acquire(stop <-chan struct{}) (release func(), ok bool) {
	release = func() {}
	if pl == nil {
		return release, true
	}
	if pl.concurrency != nil {
		select {
		case pl.concurrency <- struct{}{}:
			release = func() { <-pl.concurrency }
		case <-stop:
			return release, false
		}
	}
	if pl.rateMax > 0 {
		pl.rateMu.Lock()
		defer pl.rateMu.Unlock()
		now := time.Now()
		elapsed := now.Sub(pl.rateLast).Minutes()
		pl.rateTokens += elapsed * pl.rateMax
		if pl.rateTokens > pl.rateMax {
			pl.rateTokens = pl.rateMax
		}
		pl.rateLast = now
		if pl.rateTokens < 1 {
			return release, false
		}
		pl.rateTokens -= 1
	}
	return release, true
}

// limiterStore is the per-name registry of profileLimiters owned by Server.
type limiterStore struct {
	mu       sync.RWMutex
	limiters map[string]*profileLimiter
}

func newLimiterStore() *limiterStore {
	return &limiterStore{limiters: map[string]*profileLimiter{}}
}

// Get returns (creating if needed) the limiter for a profile, sized from
// its declared limits. The store is keyed on profile name; calling Get
// for a profile whose limits change requires invalidating via Reset.
func (s *limiterStore) Get(name string, maxConcurrent, ratePerMinute int) *profileLimiter {
	s.mu.RLock()
	if pl, ok := s.limiters[name]; ok {
		s.mu.RUnlock()
		return pl
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if pl, ok := s.limiters[name]; ok {
		return pl
	}
	pl := newProfileLimiter(maxConcurrent, ratePerMinute)
	s.limiters[name] = pl
	return pl
}

// Reset removes the limiter for a profile name; the next Get rebuilds
// from the current profile config. Called from the registry's profile-
// updated event so live edits to limits take effect.
func (s *limiterStore) Reset(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.limiters, name)
}
