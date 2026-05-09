package main

import (
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// ABTest splits traffic between two backends to compare quality and cost.
// Configure in config.yaml under ab_tests.

type ABTestConfig struct {
	Name       string  `yaml:"name" json:"name"`
	Match      RuleMatch `yaml:"match" json:"match"`
	VariantA   string  `yaml:"variant_a" json:"variant_a"`     // backend name
	VariantB   string  `yaml:"variant_b" json:"variant_b"`     // backend name
	SplitPct   int     `yaml:"split_pct" json:"split_pct"`     // % of traffic to variant B (0-100)
	Active     bool    `yaml:"active" json:"active"`
}

type ABTestResult struct {
	Name       string  `json:"name"`
	VariantA   ABVariantStats `json:"variant_a"`
	VariantB   ABVariantStats `json:"variant_b"`
}

type ABVariantStats struct {
	Backend    string  `json:"backend"`
	Requests   int64   `json:"requests"`
	AvgLatMS   float64 `json:"avg_latency_ms"`
	TotalCost  float64 `json:"total_cost"`
	AvgTokens  float64 `json:"avg_output_tokens"`
	ErrorRate  float64 `json:"error_rate"`
	totalLat   int64
	errors     int64
	totalTok   int64
}

type ABTestManager struct {
	tests   []ABTestConfig
	results map[string]*abResultPair
	mu      sync.RWMutex
}

type abResultPair struct {
	a ABVariantStats
	b ABVariantStats
}

func newABTestManager(tests []ABTestConfig) *ABTestManager {
	m := &ABTestManager{
		tests:   tests,
		results: make(map[string]*abResultPair),
	}
	for _, t := range tests {
		m.results[t.Name] = &abResultPair{
			a: ABVariantStats{Backend: t.VariantA},
			b: ABVariantStats{Backend: t.VariantB},
		}
	}
	return m
}

func (m *ABTestManager) updateTests(tests []ABTestConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tests = tests
	for _, t := range tests {
		if _, ok := m.results[t.Name]; !ok {
			m.results[t.Name] = &abResultPair{
				a: ABVariantStats{Backend: t.VariantA},
				b: ABVariantStats{Backend: t.VariantB},
			}
		}
	}
}

// SelectVariant checks if an A/B test applies and returns the selected backend.
// Returns ("", "") if no test applies.
func (m *ABTestManager) SelectVariant(meta RouteRequest) (testName, backendName string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, t := range m.tests {
		if !t.Active {
			continue
		}
		if !matchRule(&RoutingRule{Match: t.Match}, meta) {
			continue
		}
		// Randomly select variant
		if rand.Intn(100) < t.SplitPct {
			return t.Name, t.VariantB
		}
		return t.Name, t.VariantA
	}
	return "", ""
}

// RecordResult logs the outcome of an A/B test request.
func (m *ABTestManager) RecordResult(testName, backendName string, latency time.Duration, outputTokens int, cost float64, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pair, ok := m.results[testName]
	if !ok {
		return
	}

	var v *ABVariantStats
	if backendName == pair.a.Backend {
		v = &pair.a
	} else if backendName == pair.b.Backend {
		v = &pair.b
	} else {
		return
	}

	v.Requests++
	v.totalLat += latency.Milliseconds()
	v.totalTok += int64(outputTokens)
	v.TotalCost += cost
	if !success {
		v.errors++
	}

	if v.Requests > 0 {
		v.AvgLatMS = float64(v.totalLat) / float64(v.Requests)
		v.AvgTokens = float64(v.totalTok) / float64(v.Requests)
		v.ErrorRate = float64(v.errors) / float64(v.Requests)
	}
}

// Results returns all A/B test results.
func (m *ABTestManager) Results() []ABTestResult {
	m.mu.RLock()
	defer m.mu.RUnlock()

	results := make([]ABTestResult, 0)
	for name, pair := range m.results {
		results = append(results, ABTestResult{
			Name:     name,
			VariantA: pair.a,
			VariantB: pair.b,
		})
	}
	return results
}

// handleABTests returns A/B test results.
func handleABTests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, 200, abTests.Results())
}
