package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ShadowRouter mirrors a percentage of primary-backend requests to a
// shadow backend, compares the outputs, and logs the comparison. The
// shadow response is NEVER returned to the caller; only the primary's
// reply lands in the HTTP response.
//
// Operators use this to answer "what would happen if we switched
// backend A → B?" with empirical data. After enough comparisons, the
// dashboard surfaces "swapping primary to B saves $X/day at Y% similarity".
type ShadowRouter struct {
	splitPctByName map[string]int           // shadow backend name → 0-100
	configMu       sync.RWMutex             // guards splitPctByName + tests map
	tests          map[string]*ShadowTest   // test name → config
	results        chan ShadowComparison
	resultsCount   atomic.Uint64
	logPath        string
	stopCh         chan struct{}
	closeOnce      sync.Once
}

// ShadowTest describes one A/B test. Variants A and B point at named
// backends; split_pct is the percentage of requests duplicated to B.
// Mode = "shadow" (B's output is logged but not returned). "promote"
// is reserved for future use where B's output replaces A's.
type ShadowTest struct {
	Name      string `json:"name"`
	VariantA  string `json:"variant_a"`
	VariantB  string `json:"variant_b"`
	SplitPct  int    `json:"split_pct"`
	Match     map[string]string `json:"match,omitempty"`
}

// ShadowComparison is one A/B sample.
type ShadowComparison struct {
	TestName   string  `json:"test_name"`
	RequestID  string  `json:"request_id"`
	VariantA   string  `json:"variant_a"`
	VariantB   string  `json:"variant_b"`
	OutputA    string  `json:"output_a"`
	OutputB    string  `json:"output_b"`
	Similarity float64 `json:"similarity"`
	CostA      float64 `json:"cost_a_usd"`
	CostB      float64 `json:"cost_b_usd"`
	CostDelta  float64 `json:"cost_delta_usd"` // B − A; positive means shadow would have cost more
	Timestamp  string  `json:"timestamp"`
}

// NewShadowRouter constructs a router with a writer goroutine that
// appends comparisons to logPath as JSONL. Set logPath to "" for an
// in-memory-only setup (results are only kept in the metrics counter).
func NewShadowRouter(logPath string) *ShadowRouter {
	sr := &ShadowRouter{
		splitPctByName: map[string]int{},
		tests:          map[string]*ShadowTest{},
		results:        make(chan ShadowComparison, 256),
		logPath:        logPath,
		stopCh:         make(chan struct{}),
	}
	if logPath != "" {
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	}
	go sr.writerLoop()
	return sr
}

// AddTest registers an A/B test. Idempotent; updates if name exists.
func (sr *ShadowRouter) AddTest(test ShadowTest) {
	if test.Name == "" || test.SplitPct <= 0 || test.VariantB == "" {
		return
	}
	sr.configMu.Lock()
	defer sr.configMu.Unlock()
	sr.tests[test.Name] = &test
	sr.splitPctByName[test.VariantB] = test.SplitPct
}

// RemoveTest deregisters by name.
func (sr *ShadowRouter) RemoveTest(name string) {
	sr.configMu.Lock()
	defer sr.configMu.Unlock()
	if t, ok := sr.tests[name]; ok {
		delete(sr.splitPctByName, t.VariantB)
		delete(sr.tests, name)
	}
}

// MatchTest finds the first test whose Match map all matches the request
// metadata. Used by the proxy hot path to decide whether to spawn a
// shadow goroutine for this request.
func (sr *ShadowRouter) MatchTest(meta RouteRequest) *ShadowTest {
	sr.configMu.RLock()
	defer sr.configMu.RUnlock()
	for _, test := range sr.tests {
		if matchShadowTest(test, meta) {
			return test
		}
	}
	return nil
}

func matchShadowTest(test *ShadowTest, meta RouteRequest) bool {
	if v, ok := test.Match["service"]; ok && v != meta.Service {
		return false
	}
	if v, ok := test.Match["call_type"]; ok && v != meta.CallType {
		return false
	}
	if v, ok := test.Match["priority"]; ok && v != meta.Priority {
		return false
	}
	return true
}

// FireShadow runs the variant_b request in a goroutine, comparing the
// output once it returns. Caller passes the primary's already-completed
// output so we can compute similarity inline. Non-blocking; panics in
// the goroutine are caught and logged.
func (sr *ShadowRouter) FireShadow(ctx context.Context, test *ShadowTest, requestID string, body []byte, primaryOutput string, primaryCostUSD float64) {
	if sr == nil || test == nil {
		return
	}
	if !sr.shouldFire(test.SplitPct) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Printf("shadow router panic: %v", r)
			}
		}()
		shadowCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		shadowOutput, shadowCost, err := sr.invokeShadow(shadowCtx, test.VariantB, body)
		if err != nil {
			logger.Printf("shadow invocation failed test=%s backend=%s: %v", test.Name, test.VariantB, err)
			return
		}
		similarity := jaccardSimilarity(primaryOutput, shadowOutput)
		comparison := ShadowComparison{
			TestName:   test.Name,
			RequestID:  requestID,
			VariantA:   test.VariantA,
			VariantB:   test.VariantB,
			OutputA:    primaryOutput,
			OutputB:    shadowOutput,
			Similarity: similarity,
			CostA:      primaryCostUSD,
			CostB:      shadowCost,
			CostDelta:  shadowCost - primaryCostUSD,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
		}
		select {
		case sr.results <- comparison:
		default:
			// buffer full, drop and account
			logger.Printf("shadow results buffer full, dropping comparison for test=%s", test.Name)
		}
	}()
}

// shouldFire is a 0-100 percentage gate. Uses a per-call atomic counter
// so callers across goroutines don't all hit on the same percentile band.
var shadowFireCounter atomic.Uint64

func (sr *ShadowRouter) shouldFire(splitPct int) bool {
	if splitPct >= 100 {
		return true
	}
	if splitPct <= 0 {
		return false
	}
	n := shadowFireCounter.Add(1)
	return int(n%100) < splitPct
}

// invokeShadow forwards body to the named backend and returns (output,
// estimated cost USD, err). Reuses the same forwardToBackend code path
// as the primary so any provider quirks apply equally.
func (sr *ShadowRouter) invokeShadow(ctx context.Context, backendName string, body []byte) (string, float64, error) {
	if pool == nil {
		return "", 0, fmt.Errorf("backend pool not initialised")
	}
	backend := pool.Get(backendName)
	if backend == nil {
		return "", 0, fmt.Errorf("unknown shadow backend: %s", backendName)
	}
	// Use a fake metadata to get through forwardToBackend; the meta
	// fields we don't care about for shadow comparisons.
	meta := RouteRequest{}
	statusCode, _, respBody, err := forwardToBackend(backend, "", body, &ChatRequest{}, meta)
	if err != nil {
		return "", 0, err
	}
	if statusCode >= 400 {
		return "", 0, fmt.Errorf("shadow backend returned %d", statusCode)
	}
	output, _ := extractMessageContent(respBody)
	// Estimate cost: we don't get accurate token counts back without
	// parsing usage; use a rough char-based estimate (4 chars ≈ 1 token)
	// against backend's per-1M pricing.
	estIn := len(body) / 4
	estOut := len(output) / 4
	costUSD := (backend.Config.CostInput1M*float64(estIn) + backend.Config.CostOutput1M*float64(estOut)) / 1e6
	return output, costUSD, nil
}

func (sr *ShadowRouter) writerLoop() {
	for {
		select {
		case cmp, ok := <-sr.results:
			if !ok {
				return
			}
			sr.persist(cmp)
		case <-sr.stopCh:
			return
		}
	}
}

func (sr *ShadowRouter) persist(cmp ShadowComparison) {
	sr.resultsCount.Add(1)
	if sr.logPath == "" {
		return
	}
	f, err := os.OpenFile(sr.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		logger.Printf("shadow log open: %v", err)
		return
	}
	defer f.Close()
	enc, _ := json.Marshal(cmp)
	_, _ = f.Write(append(enc, '\n'))
}

// Close stops the writer goroutine.
func (sr *ShadowRouter) Close() {
	if sr == nil {
		return
	}
	sr.closeOnce.Do(func() {
		close(sr.stopCh)
	})
}

// Stats returns the marshallable snapshot.
func (sr *ShadowRouter) Stats() ShadowStats {
	if sr == nil {
		return ShadowStats{}
	}
	sr.configMu.RLock()
	tests := make([]ShadowTest, 0, len(sr.tests))
	for _, t := range sr.tests {
		tests = append(tests, *t)
	}
	sr.configMu.RUnlock()
	return ShadowStats{
		Tests:           tests,
		ResultsRecorded: sr.resultsCount.Load(),
		LogPath:         sr.logPath,
	}
}

// ShadowStats is the public marshallable view.
type ShadowStats struct {
	Tests           []ShadowTest `json:"tests"`
	ResultsRecorded uint64       `json:"results_recorded"`
	LogPath         string       `json:"log_path,omitempty"`
}

// jaccardSimilarity computes word-set overlap. Returns 0.0 to 1.0.
// Cheap and robust for similarity comparison without an embedding model.
// 0 = no shared words, 1 = identical word sets.
func jaccardSimilarity(a, b string) float64 {
	wordsA := tokeniseForSimilarity(a)
	wordsB := tokeniseForSimilarity(b)
	if len(wordsA) == 0 && len(wordsB) == 0 {
		return 1.0
	}
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0
	}
	setA := make(map[string]struct{}, len(wordsA))
	for _, w := range wordsA {
		setA[w] = struct{}{}
	}
	intersect := 0
	for _, w := range wordsB {
		if _, ok := setA[w]; ok {
			intersect++
		}
	}
	setB := make(map[string]struct{}, len(wordsB))
	for _, w := range wordsB {
		setB[w] = struct{}{}
	}
	union := len(setA)
	for w := range setB {
		if _, ok := setA[w]; !ok {
			union++
		}
	}
	if union == 0 {
		return 1.0
	}
	return float64(intersect) / float64(union)
}

func tokeniseForSimilarity(s string) []string {
	s = strings.ToLower(s)
	out := []string{}
	cur := strings.Builder{}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// shadowRouter is the package-level handle. Set in main.go from any
// `ab_tests:` config or via the API.
var shadowRouter *ShadowRouter

// handleShadowResults serves GET /api/shadow/stats.
func handleShadowResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if shadowRouter == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": false,
			"data":    []any{},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(shadowRouter.Stats())
}
