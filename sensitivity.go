package main

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Sensitivity scoring — the compliance dimension of routing.
//
// The complexity classifier (classifier.go) decides how CAPABLE a model needs
// to be. This decides how TRUSTED the endpoint must be. A prompt that carries
// personal or regulated data should stay on a trusted, self hosted model even
// when a cheaper or external endpoint would answer it, so that sensitive data
// never leaves an approved boundary.
//
// This mirrors the classifier gated routing idea (ComplianceGate, arXiv
// 2606.31163): a cheap upstream check on data sensitivity, separate from the
// cost and complexity decision. It is deliberately additive and OFF by default:
// ScoreSensitivity always runs (so the score can be logged and measured), but
// it only forces routing when KX_SENSITIVITY_ROUTING=1, so the live cost
// routing path is unchanged until the gate is explicitly enabled.
//
// When the caller sets X-Kronaxis-Tier explicitly, that still takes precedence
// upstream, exactly as it does for complexity.

// SensitivityScore is 0-100 where 0 is benign and 100 is highly sensitive
// (personal, financial, health, credential or special category data present).
type SensitivityScore float64

// SensitiveFloor is the score at or above which a request should be pinned to a
// trusted, self hosted model and kept off any external or less trusted endpoint.
const SensitiveFloor = 55.0

// AmbiguityBand is the width of the grey zone just below SensitiveFloor. A score
// in [SensitiveFloor-AmbiguityBand, SensitiveFloor) is treated as UNCERTAIN and
// fails closed to the trusted tier: when we cannot be confident a prompt is
// benign, we would rather keep it on a trusted self hosted model than risk
// leaking regulated data to a cheaper or external endpoint. This mirrors
// ComplianceGate (arXiv 2606.31163), whose classifier overrides a low confidence
// prediction to the most restrictive label. It is logged distinctly from a clear
// sensitive hit (TrustedUncertain vs TrustedSensitive) so we can measure how much
// traffic the grey zone captures before deciding whether to widen or narrow it.
const AmbiguityBand = 10.0

// piiPatterns are structured identifiers whose mere presence is a strong signal.
// UK context first, since that is the operating jurisdiction.
var piiPatterns = []struct {
	name string
	re   *regexp.Regexp
	w    float64
}{
	{"email", regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`), 18},
	{"uk_ni_number", regexp.MustCompile(`(?i)\b[A-CEGHJ-PR-TW-Z]{2}\s?\d{6}\s?[A-D]\b`), 40},
	{"uk_postcode", regexp.MustCompile(`(?i)\b[A-Z]{1,2}\d[A-Z\d]?\s?\d[A-Z]{2}\b`), 12},
	{"uk_phone", regexp.MustCompile(`\b(?:0\d{9,10}|\+44\s?\d{9,10})\b`), 16},
	{"card_number", regexp.MustCompile(`\b(?:\d[ \-]?){13,16}\b`), 40},
	{"sort_code", regexp.MustCompile(`\b\d{2}[- ]\d{2}[- ]\d{2}\b`), 25},
	{"iban", regexp.MustCompile(`(?i)\b[A-Z]{2}\d{2}[A-Z0-9]{10,30}\b`), 30},
	{"date_of_birth", regexp.MustCompile(`\b\d{1,2}[/\-.]\d{1,2}[/\-.]\d{2,4}\b`), 8},
}

// sensitiveKeywords are weighted phrases that raise the sensitivity score.
var sensitiveKeywords = []keywordWeight{
	// credentials and secrets
	{"password", 30}, {"api key", 30}, {"secret key", 30}, {"private key", 35},
	{"access token", 25}, {"credentials", 18}, {"bearer ", 18},
	// identity documents
	{"national insurance", 35}, {"ni number", 30}, {"passport number", 35},
	{"driving licence", 25}, {"social security", 30}, {"ssn", 25},
	// financial
	{"bank account", 25}, {"account number", 22}, {"sort code", 25},
	{"credit card", 30}, {"debit card", 28}, {"card number", 30}, {"cvv", 30},
	// health and special category
	{"medical record", 35}, {"diagnosis", 22}, {"prescription", 20}, {"patient", 15},
	{"nhs number", 35}, {"mental health", 22}, {"disability", 18},
	{"criminal record", 30}, {"conviction", 20}, {"immigration status", 25},
	{"asylum", 25}, {"ethnicity", 18}, {"religion", 12}, {"sexual orientation", 22},
	// explicit handling markers
	{"confidential", 15}, {"personal data", 18}, {"gdpr", 15},
	{"data subject", 18}, {"pii", 18}, {"do not share", 15},
	// person identifiers in context
	{"date of birth", 20}, {"home address", 18}, {"full name and", 14},
}

// ScoreSensitivity returns a 0-100 sensitivity score for the request. It reads
// only user and system message text; tool and assistant turns are ignored so a
// model's own earlier output does not inflate the score.
func ScoreSensitivity(req *ChatRequest) SensitivityScore {
	var text string
	for _, msg := range req.Messages {
		if msg.Role != "user" && msg.Role != "system" {
			continue
		}
		if s, ok := msg.Content.(string); ok {
			text += s + "\n"
		}
	}
	if text == "" {
		return 0
	}
	lower := strings.ToLower(text)
	raw := 0.0

	// Structured identifiers: count distinct pattern types, cap per type so a
	// list of many emails does not dominate.
	for _, p := range piiPatterns {
		if m := p.re.FindAllString(text, 3); len(m) > 0 {
			hits := float64(len(m))
			if hits > 2 {
				hits = 2
			}
			raw += p.w * (0.6 + 0.2*hits) // first hit full-ish, extra hits taper
		}
	}

	// Weighted sensitive phrases.
	for _, kw := range sensitiveKeywords {
		if strings.Contains(lower, kw.Keyword) {
			raw += kw.Weight
		}
	}

	// Very short benign prompts should not drift up on a single weak hit.
	if utf8.RuneCountInString(text) < 40 {
		raw -= 8
	}
	if raw < 0 {
		raw = 0
	}

	// Map raw (roughly 0-120+) to 0-100 with a soft ceiling.
	score := sigmoid(raw/40-1.0) * 140
	if score > 100 {
		score = 100
	}
	return SensitivityScore(score)
}

// SensitivityRoutingEnabled reports whether the sensitivity gate should force
// routing. Default OFF: the score is still computed and can be logged, but it
// does not alter model selection unless explicitly enabled.
func SensitivityRoutingEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("KX_SENSITIVITY_ROUTING")))
	return v == "1" || v == "true" || v == "on"
}

// MustStayTrusted reports whether this request carries enough sensitivity that,
// when the gate is enabled, it must be pinned to a trusted self hosted model.
// It fails closed: the grey ambiguity band counts as must stay trusted.
func MustStayTrusted(req *ChatRequest) bool {
	return SensitivityRoutingEnabled() && ClassifySensitivity(req) != RouteFree
}

// SensitivityDecision is the trust dimension of the routing decision, kept
// separate from the complexity tier so both can be logged.
type SensitivityDecision int

const (
	RouteFree        SensitivityDecision = iota // benign: any tier allowed
	TrustedUncertain                            // grey zone: fail closed to trusted
	TrustedSensitive                            // clearly sensitive: pin to trusted
)

func (d SensitivityDecision) String() string {
	switch d {
	case TrustedSensitive:
		return "trusted-sensitive"
	case TrustedUncertain:
		return "trusted-uncertain"
	default:
		return "free"
	}
}

// ClassifySensitivity maps a request to a trust decision using the score, the
// sensitive floor and the fail closed ambiguity band. This always runs so the
// decision can be logged; it only alters routing when the gate is enabled.
func ClassifySensitivity(req *ChatRequest) SensitivityDecision {
	return classifyScore(float64(ScoreSensitivity(req)))
}

func classifyScore(s float64) SensitivityDecision {
	switch {
	case s >= SensitiveFloor:
		return TrustedSensitive
	case s >= SensitiveFloor-AmbiguityBand:
		return TrustedUncertain
	default:
		return RouteFree
	}
}

// TierCostRatio describes one tier's share of traffic and its per token cost
// relative to the most capable (largest) tier, which is 1.0.
type TierCostRatio struct {
	Tier  string
	Share float64 // fraction of traffic routed to this tier, 0..1
	Ratio float64 // cost per token relative to the top tier
}

// RoutingSavings implements the ComplianceGate cost model S = 1 - sum(p_i * r_i)
// (arXiv 2606.31163): the fractional cost saving of a tiered routing
// distribution versus sending every request to the top tier. Returns a value in
// [0, 1); 0.4 means 40 percent cheaper than routing everything to the largest
// model. Shares should sum to about 1. This is for router cost reporting and
// sales collateral, not the live routing path. The default ComplianceGate ratios
// (small 0.15, mid 0.26, large 1.0) are a reasonable starting point until we
// measure our own per tier token costs.
func RoutingSavings(dist []TierCostRatio) float64 {
	weighted := 0.0
	for _, t := range dist {
		weighted += t.Share * t.Ratio
	}
	s := 1.0 - weighted
	if s < 0 {
		s = 0
	}
	return s
}
