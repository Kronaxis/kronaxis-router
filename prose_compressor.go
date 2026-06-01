package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// PromptCompressor is a learned (model-based) prose compressor. It is LOSSY by
// design — it drops low-information tokens the way LLMLingua does — so it is
// only ever used on the aggressive compress/bulk path, never the always-on
// lossless pass, and never on code or JSON segments.
//
// rate is the target fraction of the prose to KEEP (0.0–1.0); e.g. 0.5 ≈ halve.
type PromptCompressor interface {
	Compress(text string, rate float64) (string, error)
}

var (
	proseCompressOK   atomic.Uint64
	proseCompressFail atomic.Uint64
)

// httpProseCompressor calls a self-hosted learned-compression endpoint
// (services/prose-compressor, LLMLingua on a dedicated GPU). It owns its own
// timeout so callers need not thread a context; on any failure the caller falls
// back to the lexical prose passes, so a dead service degrades gracefully.
type httpProseCompressor struct {
	url    string
	client *http.Client
}

func newHTTPProseCompressor(url string, timeoutMS int) *httpProseCompressor {
	if timeoutMS <= 0 {
		timeoutMS = 8000
	}
	return &httpProseCompressor{
		url:    url,
		client: &http.Client{Timeout: time.Duration(timeoutMS) * time.Millisecond},
	}
}

type proseCompressReq struct {
	Text string  `json:"text"`
	Rate float64 `json:"rate"`
}

type proseCompressResp struct {
	Compressed string `json:"compressed"`
}

func (h *httpProseCompressor) Compress(text string, rate float64) (string, error) {
	if rate <= 0 || rate >= 1 {
		rate = 0.5
	}
	body, _ := json.Marshal(proseCompressReq{Text: text, Rate: rate})
	resp, err := h.client.Post(h.url, "application/json", bytes.NewReader(body))
	if err != nil {
		proseCompressFail.Add(1)
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		proseCompressFail.Add(1)
		return "", fmt.Errorf("prose compressor HTTP %d: %s", resp.StatusCode, string(data))
	}
	var out proseCompressResp
	if err := json.Unmarshal(data, &out); err != nil {
		proseCompressFail.Add(1)
		return "", err
	}
	proseCompressOK.Add(1)
	return out.Compressed, nil
}
