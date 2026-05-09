package main

import (
	_ "embed"
	"net/http"
)

//go:embed ui-cost-lab.html
var costLabHTML []byte

// handleCostLab serves the embedded Cost Lab dashboard. Single self
// contained HTML page that polls /api/costs, /api/costs/forecast,
// /api/shadow/stats, and /api/dpo via vanilla fetch.
func handleCostLab(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(costLabHTML)
}
