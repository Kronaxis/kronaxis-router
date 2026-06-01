package main

import (
	"net/http/httptest"
	"testing"
)

func newCCRTestMW(enabled bool, services []string) *GraphifyMiddleware {
	return &GraphifyMiddleware{cfg: GraphifyConfig{CCREnabled: enabled, CCRServices: services}}
}

func TestCCRAllowedHeaderOptIn(t *testing.T) {
	m := newCCRTestMW(true, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if m.ccrAllowed(r) {
		t.Error("unknown client must NOT be allowed to elide")
	}
	r.Header.Set("X-Kronaxis-Compress-CCR", "1")
	if !m.ccrAllowed(r) {
		t.Error("explicit opt-in header should allow elision")
	}
}

func TestCCRAllowedServiceAllowlist(t *testing.T) {
	m := newCCRTestMW(true, []string{"bulk-extractor"})
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Kronaxis-Service", "Bulk-Extractor") // case-insensitive
	if !m.ccrAllowed(r) {
		t.Error("allowlisted service should be allowed")
	}
	r2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r2.Header.Set("X-Kronaxis-Service", "random-agent")
	if m.ccrAllowed(r2) {
		t.Error("non-allowlisted service must NOT be allowed")
	}
}

func TestCCRAllowedRequiresEnabled(t *testing.T) {
	m := newCCRTestMW(false, []string{"bulk-extractor"})
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Kronaxis-Compress-CCR", "1")
	r.Header.Set("X-Kronaxis-Service", "bulk-extractor")
	if m.ccrAllowed(r) {
		t.Error("CCR disabled in config must override any opt-in")
	}
}
