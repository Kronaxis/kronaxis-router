package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Adversarial consensus (ROADMAP #18). Opt-in via X-Kronaxis-Consensus: 1.
// The request is dispatched to several backends concurrently; if their answers
// agree (Jaccard similarity over the configured threshold) the agreed answer is
// returned, otherwise an arbiter model resolves the disagreement. Costs N×+1
// calls, so opt-in only and never on streaming.

var (
	consensusAgreedTotal     atomic.Uint64
	consensusArbitratedTotal atomic.Uint64
)

const (
	defaultConsensusN     = 3
	defaultConsensusAgree = 0.8
)

// runConsensus dispatches to up to N candidates concurrently and returns
// (responseBody, statusCode, mode) where mode is "agreed", "arbitrated", or ""
// (couldn't run — caller should fall back to normal dispatch). A nil body also
// signals fall-back.
func runConsensus(req *ChatRequest, body []byte, meta RouteRequest, candidates []RouteResult) ([]byte, int, string) {
	n := defaultConsensusN
	if n > len(candidates) {
		n = len(candidates)
	}
	if n < 2 {
		return nil, 0, "" // need at least two opinions
	}
	picks := candidates[:n]

	type res struct {
		body    []byte
		status  int
		content string
		err     error
	}
	results := make([]res, n)
	var wg sync.WaitGroup
	for i := range picks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, _, b, err := forwardToBackend(picks[i].Backend, picks[i].ModelName, body, req, meta)
			results[i] = res{body: b, status: st, content: extractContent(b), err: err}
		}(i)
	}
	wg.Wait()

	// Keep the successful, non-empty answers.
	var ok []res
	for _, r := range results {
		if r.err == nil && r.status < 400 && r.content != "" {
			ok = append(ok, r)
		}
	}
	if len(ok) == 0 {
		return nil, 0, "" // all failed; let caller do normal dispatch
	}
	if len(ok) == 1 {
		return ok[0].body, ok[0].status, "agreed" // only one opinion survived
	}

	// Agreement: every pair must be at least `agree` similar.
	agree := defaultConsensusAgree
	agreed := true
	for i := 0; i < len(ok) && agreed; i++ {
		for j := i + 1; j < len(ok); j++ {
			if jaccardSimilarity(ok[i].content, ok[j].content) < agree {
				agreed = false
				break
			}
		}
	}
	if agreed {
		consensusAgreedTotal.Add(1)
		return ok[0].body, ok[0].status, "agreed"
	}

	// Divergence: ask an arbiter to resolve.
	arb := consensusArbiter(picks)
	if arb == nil {
		// No arbiter available: return the first opinion rather than fail.
		return ok[0].body, ok[0].status, "agreed"
	}
	question := lastUserMessage(req)
	var b strings.Builder
	fmt.Fprintf(&b, "%d models were asked the same question and disagreed. Here are their answers:\n\n", len(ok))
	for i, r := range ok {
		fmt.Fprintf(&b, "--- Answer %d ---\n%s\n\n", i+1, r.content)
	}
	b.WriteString("Resolve the disagreement and produce the single correct, final answer. Reply with ONLY that answer.")

	arbReq := *req
	arbReq.Stream = false
	arbReq.Messages = []ChatMessage{
		{Role: "user", Content: question + "\n\n" + b.String()},
	}
	arbBody, err := json.Marshal(arbReq)
	if err != nil {
		return ok[0].body, ok[0].status, "agreed"
	}
	st, _, arbResp, err := forwardToBackend(arb, arb.Config.ModelName, arbBody, &arbReq, meta)
	if err != nil || st >= 400 || extractContent(arbResp) == "" {
		return ok[0].body, ok[0].status, "agreed"
	}
	consensusArbitratedTotal.Add(1)
	return arbResp, st, "arbitrated"
}

// consensusArbiter resolves the arbiter backend: the configured
// server.consensus_arbiter if available, else the first candidate's backend.
func consensusArbiter(picks []RouteResult) *Backend {
	if cfg != nil && cfg.Server.ConsensusArbiter != "" {
		if b := pool.Get(cfg.Server.ConsensusArbiter); b != nil && b.IsAvailable() {
			return b
		}
	}
	if len(picks) > 0 {
		return picks[0].Backend
	}
	return nil
}
