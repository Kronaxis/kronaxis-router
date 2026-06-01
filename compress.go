package main

import (
	"fmt"
	"strings"
)

// Content-aware prompt compression. A clean-room Go take on headroom's
// ContentRouter idea (https://github.com/chopratejas/headroom, Apache-2.0):
// detect what each segment of a prompt actually is — fenced code, structured
// JSON, or prose — and apply the right compressor instead of one lossy pass
// over everything. No headroom source was copied; see NOTICE.
//
// The structural passes (JSON compaction, code comment stripping) are
// near-lossless and run regardless of any token budget. truncateMiddle is the
// only lossy fallback and runs only when maxTokens > 0 and the result is still
// over budget.

// CompressStats reports what CompressContentAware did, for metrics/observability.
type CompressStats struct {
	OriginalTokens int
	FinalTokens    int
	Saved          int // OriginalTokens - FinalTokens, floored at 0
	JSONBlocks     int
	CodeBlocks     int
	ProseBlocks    int
	Truncated      bool
	Elided         int // segments stashed into the CCR store as retrieval stubs
	LearnedProse   int // prose segments compressed by the learned model
}

// CompressOpts selects which transforms run. Two ready-made profiles:
//   - losslessCompressOpts(): safe for an always-on pass over all traffic.
//     JSON compaction + prose whitespace only; keeps comments, no dedup, no
//     truncation, no CCR.
//   - fullCompressOpts(...): the aggressive bulk/background profile.
type CompressOpts struct {
	MaxTokens      int       // >0 enables truncateMiddle fallback
	DropJSONNulls  bool      // prune null/empty JSON fields (lossy)
	Tabularize     bool      // hoist repeated keys from arrays of objects
	StripComments  bool      // remove code comments (safe languages only)
	ProsePasses    bool      // markdown-noise strip + line dedup on prose (lossy-ish)
	TrimWhitespace bool      // collapse blank lines + trailing ws on prose (lossless)
	CCR            *CCRStore // if set, oversized segments are stashed + stubbed
	CCRThreshold   int       // chars; segment bodies larger than this are elided (0=off)

	// LearnedProse, if set, runs a model-based (LLMLingua-style) compressor over
	// prose segments after the lexical passes. LOSSY; full profile only. On any
	// error the lexical result is kept, so a dead service degrades gracefully.
	LearnedProse        PromptCompressor
	LearnedProseRate    float64 // target fraction to keep (0–1); 0 → 0.5
	LearnedProseMinChar int     // skip segments smaller than this
}

func losslessCompressOpts() CompressOpts {
	return CompressOpts{TrimWhitespace: true}
}

func fullCompressOpts(maxTokens int, dropNulls, tabularize bool, ccr *CCRStore, ccrThreshold int) CompressOpts {
	return CompressOpts{
		MaxTokens:      maxTokens,
		DropJSONNulls:  dropNulls,
		Tabularize:     tabularize,
		StripComments:  true,
		ProsePasses:    true,
		TrimWhitespace: true,
		CCR:            ccr,
		CCRThreshold:   ccrThreshold,
	}
}

type segKind int

const (
	kindProse segKind = iota
	kindCode
)

type segment struct {
	kind segKind
	lang string // fenced-block language tag (code segments only)
	body string
}

// CompressContentAware routes each segment of text to the appropriate
// compressor under the given options, then optionally truncates to a budget.
// It never returns text larger than the input.
func CompressContentAware(text string, opts CompressOpts) (string, CompressStats) {
	stats := CompressStats{OriginalTokens: CountTokens(text)}

	segs := splitFences(text)
	var out []string
	for _, s := range segs {
		switch s.kind {
		case kindCode:
			body := s.body
			if isJSONLang(s.lang) || looksLikeJSON(body) {
				if crushed, ok := crushJSONOpts(body, opts.DropJSONNulls, opts.Tabularize); ok {
					body = crushed
					stats.JSONBlocks++
				} else if opts.StripComments {
					body = compressCode(body, s.lang)
					stats.CodeBlocks++
				}
			} else if opts.StripComments {
				body = compressCode(body, s.lang)
				stats.CodeBlocks++
			}
			body = maybeElide(body, s.lang, &opts, &stats)
			out = append(out, "```"+s.lang)
			out = append(out, body)
			out = append(out, "```")
		case kindProse:
			body := s.body
			if looksLikeJSON(body) {
				if crushed, ok := crushJSONOpts(body, opts.DropJSONNulls, opts.Tabularize); ok {
					body = crushed
					stats.JSONBlocks++
				} else {
					body = compressProseWith(body, opts)
					body = maybeLearnedProse(body, &opts, &stats)
					stats.ProseBlocks++
				}
			} else {
				body = compressProseWith(body, opts)
				body = maybeLearnedProse(body, &opts, &stats)
				stats.ProseBlocks++
			}
			body = maybeElide(body, "", &opts, &stats)
			out = append(out, body)
		}
	}

	compressed := strings.Join(out, "\n")

	if opts.MaxTokens > 0 && CountTokens(compressed) > opts.MaxTokens {
		compressed = truncateMiddle(compressed, opts.MaxTokens)
		stats.Truncated = true
	}

	stats.FinalTokens = CountTokens(compressed)
	stats.Saved = stats.OriginalTokens - stats.FinalTokens
	if stats.Saved < 0 {
		// Compression somehow inflated the text (rare; e.g. dominated by tiny
		// prose). Return the original to guarantee we never make things worse.
		stats.Saved = 0
		stats.FinalTokens = stats.OriginalTokens
		return text, stats
	}
	return compressed, stats
}

// maybeElide stashes an oversized segment body into the CCR store and returns a
// compact retrieval stub in its place. Returns body unchanged if CCR is off or
// the body is under threshold.
func maybeElide(body, lang string, opts *CompressOpts, stats *CompressStats) string {
	if opts.CCR == nil || opts.CCRThreshold <= 0 || len(body) <= opts.CCRThreshold {
		return body
	}
	id := opts.CCR.Put(body)
	stats.Elided++
	preview := previewLine(body, 160)
	tag := "headroom-elided"
	if lang != "" {
		tag += " lang=" + lang
	}
	return fmt.Sprintf("[%s id=%s bytes=%d — retrieve full content via the compress_retrieve tool or GET /v1/compress/retrieve?id=%s]\npreview: %s",
		tag, id, len(body), id, preview)
}

// maybeLearnedProse runs the model-based prose compressor when configured and
// the segment is large enough. Lossy; on any failure or non-shrinking result it
// keeps the lexical body, so a dead service never breaks a request.
func maybeLearnedProse(body string, opts *CompressOpts, stats *CompressStats) string {
	if opts.LearnedProse == nil || len(body) < opts.LearnedProseMinChar {
		return body
	}
	out, err := opts.LearnedProse.Compress(body, opts.LearnedProseRate)
	if err != nil || out == "" || len(out) >= len(body) {
		return body
	}
	stats.LearnedProse++
	return out
}

func previewLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// compressProseWith applies prose transforms according to opts: full lexical
// passes if ProsePasses, otherwise just lossless whitespace collapse if
// TrimWhitespace, otherwise nothing.
func compressProseWith(s string, opts CompressOpts) string {
	switch {
	case opts.ProsePasses:
		return compressProse(s)
	case opts.TrimWhitespace:
		return collapseWhitespace(s)
	default:
		return s
	}
}

// CompressPrompt is the backward-compatible entry point: aggressive
// content-aware compression with a token budget, returning text and savings.
func CompressPrompt(text string, maxTokens int) (string, int) {
	compressed, stats := CompressContentAware(text, fullCompressOpts(maxTokens, false, false, nil, 0))
	return compressed, stats.Saved
}

// isJSONLang reports whether a fenced-block language tag denotes JSON.
func isJSONLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "json", "json5", "jsonl", "ndjson":
		return true
	default:
		return false
	}
}

// splitFences breaks text into alternating prose and fenced-code segments.
// A fence is a line whose trimmed form starts with ```; the closing fence is a
// line whose trimmed form is exactly ```. An unterminated fence runs to EOF.
func splitFences(text string) []segment {
	lines := strings.Split(text, "\n")
	var segs []segment
	var prose []string

	flushProse := func() {
		if len(prose) > 0 {
			segs = append(segs, segment{kind: kindProse, body: strings.Join(prose, "\n")})
			prose = nil
		}
	}

	i := 0
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "```") {
			lang := strings.TrimSpace(strings.TrimPrefix(t, "```"))
			i++
			var code []string
			for i < len(lines) && strings.TrimSpace(lines[i]) != "```" {
				code = append(code, lines[i])
				i++
			}
			closed := i < len(lines)
			flushProse()
			segs = append(segs, segment{kind: kindCode, lang: lang, body: strings.Join(code, "\n")})
			if closed {
				i++ // consume the closing fence
			}
			continue
		}
		prose = append(prose, lines[i])
		i++
	}
	flushProse()
	return segs
}

// compressProse applies the lexical passes to free-form text only. These are
// deliberately kept away from code segments: stripMarkdownNoise rewrites
// "# heading" lines, which would corrupt Python/shell `#` comments if applied
// to code.
func compressProse(s string) string {
	s = collapseWhitespace(s)
	s = stripMarkdownNoise(s)
	s = deduplicateLines(s)
	return s
}

// collapseWhitespace normalises excessive whitespace.
func collapseWhitespace(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// stripMarkdownNoise removes decorative markdown that consumes tokens but adds
// no semantic value. Applied to prose only (see compressProse).
func stripMarkdownNoise(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" || trimmed == "===" || trimmed == "***" || trimmed == "___" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") ||
			strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "#### ") {
			line = strings.TrimLeft(trimmed, "# ")
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// deduplicateLines removes exact duplicate non-blank lines (keeps first).
func deduplicateLines(s string) string {
	lines := strings.Split(s, "\n")
	seen := make(map[string]bool)
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			result = append(result, line)
			continue
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// truncateMiddle keeps the head and tail of the text, replacing the middle with
// a marker. Lossy fallback of last resort.
func truncateMiddle(s string, maxTokens int) string {
	runes := []rune(s)
	totalRunes := len(runes)

	charsPerToken := 4
	if totalRunes > 0 {
		currentTokens := CountTokens(s)
		if currentTokens > 0 {
			charsPerToken = totalRunes / currentTokens
			if charsPerToken < 1 {
				charsPerToken = 1
			}
		}
	}

	targetChars := maxTokens * charsPerToken
	if targetChars >= totalRunes {
		return s
	}

	headChars := targetChars * 60 / 100
	tailChars := targetChars - headChars - 30 // 30 runes for the marker

	if headChars < 0 || tailChars < 0 {
		if targetChars > totalRunes {
			targetChars = totalRunes
		}
		return string(runes[:targetChars])
	}

	head := string(runes[:headChars])
	tail := string(runes[totalRunes-tailChars:])
	return head + "\n[... truncated for cost optimisation ...]\n" + tail
}
