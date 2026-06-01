package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// Spot-market arbitrage (ROADMAP #14). A periodic price feed lets effective
// per-backend cost track live provider pricing; when cost_aware_routing is on,
// routing prefers the cheapest eligible backend (after health/SLA/cost filters).
//
// Honest framing: LLM prices don't move per-second, so this is a periodic
// refresh + cheapest-that-meets-SLA, not a high-frequency order book. The feed
// is an operator-supplied JSON map keyed by backend name:
//
//	{ "cloud-fast": {"input_1m": 2.5, "output_1m": 10.0}, ... }

type backendPrice struct {
	Input1M  float64 `json:"input_1m"`
	Output1M float64 `json:"output_1m"`
}

var (
	priceMu          sync.RWMutex
	priceOverrides   = map[string]backendPrice{}
	costAwareRouting bool
)

// EffectiveCost returns the backend's current input cost per 1M tokens: the
// live price-feed override if present, else the static config cost.
func (b *Backend) EffectiveCost() float64 {
	priceMu.RLock()
	p, ok := priceOverrides[b.Config.Name]
	priceMu.RUnlock()
	if ok {
		return p.Input1M
	}
	return b.Config.CostInput1M
}

// PriceFeed polls an operator-supplied pricing endpoint on an interval.
type PriceFeed struct {
	url      string
	interval time.Duration
	client   *http.Client
}

func newPriceFeed(url string, interval time.Duration) *PriceFeed {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &PriceFeed{url: url, interval: interval, client: &http.Client{Timeout: 10 * time.Second}}
}

func (pf *PriceFeed) Run(ctx context.Context) {
	pf.refresh(ctx)
	ticker := time.NewTicker(pf.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pf.refresh(ctx)
		}
	}
}

func (pf *PriceFeed) refresh(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pf.url, nil)
	if err != nil {
		return
	}
	resp, err := pf.client.Do(req)
	if err != nil {
		logger.Printf("price feed: fetch failed (%v); keeping last prices", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Printf("price feed: HTTP %d; keeping last prices", resp.StatusCode)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return
	}
	m, err := parsePriceFeed(data)
	if err != nil {
		logger.Printf("price feed: parse failed (%v); keeping last prices", err)
		return
	}
	priceMu.Lock()
	priceOverrides = m
	priceMu.Unlock()
	logger.Printf("price feed: updated %d backend prices", len(m))
}

func parsePriceFeed(data []byte) (map[string]backendPrice, error) {
	var m map[string]backendPrice
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
