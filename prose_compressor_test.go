package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubCompressor lets tests drive the learned-prose path without a real model.
type stubCompressor struct {
	out string
	err error
}

func (s stubCompressor) Compress(text string, rate float64) (string, error) {
	return s.out, s.err
}

func TestLearnedProseUsedAndCounted(t *testing.T) {
	opts := fullCompressOpts(0, false, false, nil, 0)
	opts.LearnedProse = stubCompressor{out: "short summary"}
	opts.LearnedProseMinChar = 10
	long := strings.Repeat("verbose explanatory sentence. ", 30)
	out, stats := CompressContentAware(long, opts)
	if stats.LearnedProse != 1 {
		t.Errorf("expected learned-prose to run once; stats=%+v", stats)
	}
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected learned output; got %q", out)
	}
}

func TestLearnedProseFallsBackOnError(t *testing.T) {
	opts := fullCompressOpts(0, false, false, nil, 0)
	opts.LearnedProse = stubCompressor{err: http.ErrServerClosed}
	opts.LearnedProseMinChar = 10
	long := strings.Repeat("verbose explanatory sentence. ", 30)
	out, stats := CompressContentAware(long, opts)
	if stats.LearnedProse != 0 {
		t.Errorf("error path must not count as learned; stats=%+v", stats)
	}
	if out == "" {
		t.Error("must keep lexical body when learned compressor errors")
	}
}

func TestLearnedProseSkipsSmall(t *testing.T) {
	opts := fullCompressOpts(0, false, false, nil, 0)
	opts.LearnedProse = stubCompressor{out: "x"}
	opts.LearnedProseMinChar = 10000 // larger than the body
	out, stats := CompressContentAware("tiny bit of prose", opts)
	if stats.LearnedProse != 0 {
		t.Errorf("should skip small prose; stats=%+v", stats)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestLearnedProseNotInLosslessProfile(t *testing.T) {
	// The lossless profile must never carry a learned (lossy) compressor.
	o := losslessCompressOpts()
	if o.LearnedProse != nil {
		t.Error("lossless profile must not set a learned compressor")
	}
}

func TestHTTPProseCompressorRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proseCompressReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Rate <= 0 || req.Rate >= 1 {
			t.Errorf("bad rate forwarded: %v", req.Rate)
		}
		_ = json.NewEncoder(w).Encode(proseCompressResp{Compressed: "COMPRESSED:" + req.Text[:5]})
	}))
	defer srv.Close()

	c := newHTTPProseCompressor(srv.URL, 2000)
	got, err := c.Compress("hello world this is prose", 0.5)
	if err != nil {
		t.Fatalf("Compress error: %v", err)
	}
	if !strings.HasPrefix(got, "COMPRESSED:") {
		t.Errorf("unexpected response: %q", got)
	}
}

func TestHTTPProseCompressorHandles500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model OOM", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newHTTPProseCompressor(srv.URL, 2000)
	if _, err := c.Compress("some prose here", 0.5); err == nil {
		t.Error("expected error on HTTP 500")
	}
}
