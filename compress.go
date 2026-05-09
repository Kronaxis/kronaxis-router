package main

import (
	"strings"
	"unicode/utf8"
)

// CompressPrompt reduces token count of a prompt by removing redundancy.
// Applied automatically to background/bulk requests when the prompt exceeds
// a configured token threshold. Interactive/normal requests are never compressed.
//
// Techniques:
// 1. Remove excessive whitespace (multiple spaces, blank lines)
// 2. Collapse repeated instructions
// 3. Truncate very long context blocks (keep head + tail)
// 4. Remove markdown formatting noise (decorative headers, horizontal rules)
//
// Returns the compressed text and estimated token savings.
func CompressPrompt(text string, maxTokens int) (string, int) {
	originalTokens := CountTokens(text)
	if maxTokens <= 0 || originalTokens <= maxTokens {
		return text, 0
	}

	compressed := text

	// Step 1: Normalise whitespace
	compressed = collapseWhitespace(compressed)

	// Step 2: Remove markdown noise
	compressed = stripMarkdownNoise(compressed)

	// Step 3: Deduplicate repeated lines
	compressed = deduplicateLines(compressed)

	// Step 4: If still over budget, truncate middle (keep head + tail)
	if CountTokens(compressed) > maxTokens {
		compressed = truncateMiddle(compressed, maxTokens)
	}

	newTokens := CountTokens(compressed)
	savings := originalTokens - newTokens
	if savings < 0 {
		savings = 0
	}
	return compressed, savings
}

// collapseWhitespace normalises excessive whitespace.
func collapseWhitespace(s string) string {
	// Collapse multiple blank lines to one
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	// Collapse multiple spaces to one
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	// Trim trailing whitespace per line
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// stripMarkdownNoise removes decorative markdown elements that consume tokens
// but add no semantic value to an LLM.
func stripMarkdownNoise(s string) string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip horizontal rules
		if trimmed == "---" || trimmed == "===" || trimmed == "***" || trimmed == "___" {
			continue
		}
		// Simplify headers (remove # prefix, keep text)
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") ||
			strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "#### ") {
			line = strings.TrimLeft(trimmed, "# ")
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// deduplicateLines removes exact duplicate lines (keeps first occurrence).
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

// truncateMiddle keeps the first and last portions of the text,
// replacing the middle with a "[... truncated ...]" marker.
func truncateMiddle(s string, maxTokens int) string {
	runes := []rune(s)
	totalRunes := len(runes)

	// Estimate chars per token
	charsPerToken := 4
	if totalRunes > 0 {
		currentTokens := CountTokens(s)
		if currentTokens > 0 {
			charsPerToken = totalRunes / currentTokens
		}
	}

	targetChars := maxTokens * charsPerToken
	if targetChars >= totalRunes {
		return s
	}

	// Keep 60% from head, 40% from tail
	headChars := targetChars * 60 / 100
	tailChars := targetChars - headChars - 30 // 30 chars for the marker

	if headChars < 0 || tailChars < 0 {
		return string(runes[:targetChars])
	}

	head := string(runes[:headChars])
	tail := string(runes[utf8.RuneCountInString(s)-tailChars:])

	return head + "\n[... truncated for cost optimisation ...]\n" + tail
}
